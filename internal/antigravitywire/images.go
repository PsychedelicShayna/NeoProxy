package antigravitywire

// Go port of NeoPi's Claude image clamp (pi-ai providers/anthropic-images.ts:
// prepareAnthropicImageContext and resizeAnthropicImageBlock). Every wire that
// delivers images to a Claude reader shares one contract; on the Cloud Code
// Assist wire it applies to Claude models whose resolved policy declares the
// host's limits. NeoPi enforces it before the request leaves the client, so
// that an oversized block can never poison every retry and model fallback.
//
// The policy mirrors NeoPi decision for decision:
//
//   - more than ManyImageThreshold images in one request tighten the pixel cap
//     to min(ManyImageMaxDimension, host cap) because they share one request
//     budget (CapsForCount; NeoPi counts the image blocks of user, developer
//     and toolResult messages, the caller counts its decoded image blocks);
//   - an image is touched only when a side exceeds the pixel cap or its base64
//     string is longer than floor(maxPayloadBytes*0.9);
//   - the first pass scales by min(1, cap/width, cap/height) and every further
//     pass (ResizeAttempts in total) shrinks by
//     max(0.25, sqrt(budget/encoded)*0.9) until the base64 length, derived
//     from the encoded byte count, fits the budget or the image is 1x1;
//   - each pass encodes PNG and JPEG (quality 85) and keeps PNG unless it is
//     strictly larger than the JPEG;
//   - an image already inside the pixel cap is only replaced if it shrank;
//   - a replaced block carries no url or provider file reference;
//   - anything that cannot be decoded or encoded stays byte-identical.
//
// Documented divergences from NeoPi, which resizes with Bun.Image:
//
//   - The bytes of a RESIZED image differ: NeoPi resamples and encodes with
//     Bun's native codecs, the port with the standard library (area-averaging
//     downscale, image/png default compression, image/jpeg). Dimensions,
//     mime-type choice for decisive inputs and every touch/skip decision are
//     identical (see testdata/capture_anthropic_images.ts); the number of
//     shrink passes needed for a payload-heavy image depends on encoder output
//     sizes and can therefore differ.
//   - Only PNG, JPEG and GIF (first frame) decode here. WebP, BMP, TIFF, HEIC
//     and AVIF, which Bun reads, stay untouched like any undecodable image.
//   - Images above maxDecodePixels are left untouched instead of being
//     decoded, so a hostile request cannot exhaust the proxy's memory.
//   - NeoPi memoizes results per block identity; a proxy request has no
//     identity to key on, so every call recomputes.
//   - Alpha is not flattened before JPEG encoding (Bun's behavior is
//     unspecified); PNG keeps it.

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	_ "image/gif" // registers the GIF decoder for image.Decode
	"image/jpeg"
	"image/png"
	"math"
)

// Canonical Anthropic Messages API contract, used when policy names no other.
const (
	// DefaultMaxImageDimension is the per-side pixel cap of the Anthropic API.
	DefaultMaxImageDimension = 8000
	// DefaultMaxImagePayloadBytes is the per-block base64 payload cap (10 MiB),
	// measured on the encoded string rather than the decoded bytes.
	DefaultMaxImagePayloadBytes = 10 * 1024 * 1024

	// ManyImageThreshold is the image count above which the tighter
	// ManyImageMaxDimension applies.
	ManyImageThreshold = 20
	// ManyImageMaxDimension is the pixel cap used above the threshold.
	ManyImageMaxDimension = 2000

	// ImagePayloadHeadroom leaves room for the surrounding JSON and any
	// provider-side accounting.
	ImagePayloadHeadroom = 0.9
	// ImageResizeAttempts is the number of re-encode passes allowed to bring
	// one image under the byte budget.
	ImageResizeAttempts = 4

	// maxDecodePixels bounds what ClampImages is willing to decode.
	maxDecodePixels = 1 << 28
	jpegQuality     = 85
)

// Image is one decoded image block of a request. Data is base64 without a
// data-URL prefix. URL and ProviderFile mirror NeoPi's ImageContent fields:
// they reference the original bytes and are dropped when a block is replaced.
type Image struct {
	Data         string
	MimeType     string
	URL          string
	ProviderFile any
}

// ImageCaps bounds the images of one request. MaxDimension is the per-side
// pixel cap; MaxPayloadBytes is the policy's payload cap BEFORE headroom (the
// budget is floor(MaxPayloadBytes*ImagePayloadHeadroom)). Zero selects the
// canonical Anthropic defaults.
type ImageCaps struct {
	MaxDimension    int
	MaxPayloadBytes int
}

// CapsForCount resolves the caps for a request carrying count images. The
// host values come from resolved model policy (max-image-dimension,
// max-image-payload-bytes); zero means unset. Many images share one request
// size budget, so above ManyImageThreshold the pixel cap tightens to
// min(ManyImageMaxDimension, host cap); a smaller set only has to clear the
// host's per-image limit.
func CapsForCount(count, maxDimension, maxPayloadBytes int) ImageCaps {
	if maxDimension <= 0 {
		maxDimension = DefaultMaxImageDimension
	}
	if maxPayloadBytes <= 0 {
		maxPayloadBytes = DefaultMaxImagePayloadBytes
	}
	if count > ManyImageThreshold {
		maxDimension = min(ManyImageMaxDimension, maxDimension)
	}
	return ImageCaps{MaxDimension: maxDimension, MaxPayloadBytes: maxPayloadBytes}
}

// ClampRequestImages applies CapsForCount(len(images), ...) and clamps the
// images in place. images is every image block the request would deliver to
// the Claude reader (NeoPi: user, developer and toolResult image blocks).
// It returns the number of blocks replaced.
func ClampRequestImages(images []*Image, maxDimension, maxPayloadBytes int) int {
	return ClampImages(images, CapsForCount(len(images), maxDimension, maxPayloadBytes))
}

// ClampImages brings every image within caps, mutating the blocks it replaces
// in place (Data, MimeType; URL and ProviderFile are cleared) and leaving all
// others byte-identical. It returns the number of blocks replaced.
func ClampImages(images []*Image, caps ImageCaps) int {
	if caps.MaxDimension <= 0 {
		caps.MaxDimension = DefaultMaxImageDimension
	}
	if caps.MaxPayloadBytes <= 0 {
		caps.MaxPayloadBytes = DefaultMaxImagePayloadBytes
	}
	budget := int(math.Floor(float64(caps.MaxPayloadBytes) * ImagePayloadHeadroom))
	changed := 0
	for _, img := range images {
		if img == nil {
			continue
		}
		data, mimeType, ok := resizeImage(img.Data, caps.MaxDimension, budget)
		if !ok {
			continue
		}
		img.Data = data
		img.MimeType = mimeType
		img.URL = ""
		img.ProviderFile = nil
		changed++
	}
	return changed
}

// resizeImage is resizeAnthropicImageBlock: ok reports a replacement.
func resizeImage(b64 string, maxDimension, budget int) (data, mimeType string, ok bool) {
	raw := decodeBase64Lenient(b64)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return "", "", false
	}
	width, height := cfg.Width, cfg.Height
	overDimension := width > maxDimension || height > maxDimension
	overBudget := len(b64) > budget
	if !overDimension && !overBudget {
		return "", "", false
	}
	if int64(width)*int64(height) > maxDecodePixels {
		return "", "", false
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", "", false
	}

	// Fitting the pixel cap does not imply fitting the byte cap: a dense
	// screenshot re-encodes large even at the cap. Shrink by the overshoot in
	// area terms until the payload fits or the passes run out.
	scale := math.Min(1, math.Min(float64(maxDimension)/float64(width), float64(maxDimension)/float64(height)))
	var best []byte
	var bestMime string
	for attempt := 0; attempt < ImageResizeAttempts; attempt++ {
		targetWidth := max(1, min(maxDimension, jsRound(float64(width)*scale)))
		targetHeight := max(1, min(maxDimension, jsRound(float64(height)*scale)))
		scaled := src
		if targetWidth != width || targetHeight != height {
			scaled = downscale(src, targetWidth, targetHeight)
		}
		pngBytes, pngErr := encodePNG(scaled)
		jpegBytes, jpegErr := encodeJPEG(scaled)
		if pngErr != nil || jpegErr != nil {
			return "", "", false
		}
		if len(pngBytes) <= len(jpegBytes) {
			best, bestMime = pngBytes, "image/png"
		} else {
			best, bestMime = jpegBytes, "image/jpeg"
		}

		// Base64 expands three bytes into four characters; the encoded length
		// is what Anthropic measures, so derive it without encoding.
		encoded := (len(best) + 2) / 3 * 4
		if encoded <= budget {
			break
		}
		if targetWidth == 1 && targetHeight == 1 {
			break
		}
		scale *= math.Max(0.25, math.Sqrt(float64(budget)/float64(encoded))*0.9)
	}
	if best == nil {
		return "", "", false
	}

	data = base64.StdEncoding.EncodeToString(best)
	// An image inside the pixel cap is only worth replacing if it shrank.
	if !overDimension && len(data) >= len(b64) {
		return "", "", false
	}
	return data, bestMime, true
}

// jsRound is JavaScript's Math.round (half up) for non-negative values.
func jsRound(v float64) int { return int(math.Floor(v + 0.5)) }

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeBase64Lenient mirrors Node's Buffer.from(s, "base64"): both the
// standard and URL-safe alphabets are accepted, other characters (whitespace,
// line breaks) are skipped and decoding stops at the first padding character.
func decodeBase64Lenient(s string) []byte {
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		return raw
	}
	clean := make([]byte, 0, len(s))
scan:
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/':
			clean = append(clean, c)
		case c == '-':
			clean = append(clean, '+')
		case c == '_':
			clean = append(clean, '/')
		case c == '=':
			break scan
		}
	}
	if len(clean)%4 == 1 {
		clean = clean[:len(clean)-1]
	}
	raw := make([]byte, base64.RawStdEncoding.DecodedLen(len(clean)))
	n, _ := base64.RawStdEncoding.Decode(raw, clean)
	return raw[:n]
}

// downscale resamples src to tw x th by exact area averaging (a box filter
// with fractional edge coverage), the standard-library stand-in for Bun's
// resampler. tw and th never exceed the source size here. Colour is averaged
// alpha-weighted so transparent pixels do not bleed. Source rows stream
// through two accumulator rows, so memory stays proportional to the output
// width rather than the surface.
func downscale(src image.Image, tw, th int) *image.NRGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, tw, th))

	colScale := float64(sw) / float64(tw)
	type span struct {
		first   int
		weights []float32
	}
	cols := make([]span, tw)
	for x := range cols {
		lo := float64(x) * colScale
		hi := float64(x+1) * colScale
		if x == tw-1 {
			hi = float64(sw)
		}
		first := int(math.Floor(lo))
		last := min(sw-1, int(math.Ceil(hi))-1)
		w := make([]float32, 0, last-first+1)
		for i := first; i <= last; i++ {
			w = append(w, float32((math.Min(hi, float64(i+1))-math.Max(lo, float64(i)))/colScale))
		}
		cols[x] = span{first: first, weights: w}
	}

	row := make([]uint8, sw*4)
	hrow := make([]float32, tw*4)
	acc := [2][]float32{make([]float32, tw*4), make([]float32, tw*4)}
	base := 0 // output row held by acc[0]

	flush := func() {
		a := acc[0]
		dst := out.Pix[base*out.Stride : base*out.Stride+tw*4]
		for x := 0; x < tw; x++ {
			ra, ga, ba, aa := a[x*4], a[x*4+1], a[x*4+2], a[x*4+3]
			if aa > 0 {
				dst[x*4] = clampByte(ra / aa)
				dst[x*4+1] = clampByte(ga / aa)
				dst[x*4+2] = clampByte(ba / aa)
				dst[x*4+3] = clampByte(aa)
			}
		}
		acc[0], acc[1] = acc[1], acc[0]
		clear(acc[1])
		base++
	}

	rowScale := float64(sh) / float64(th)
	for y := 0; y < sh; y++ {
		readRow(src, b.Min.X, b.Min.Y+y, sw, row)
		for x, c := range cols {
			var r, g, bl, a float32
			for i, w := range c.weights {
				px := row[(c.first+i)*4 : (c.first+i)*4+4]
				pa := float32(px[3])
				r += w * float32(px[0]) * pa
				g += w * float32(px[1]) * pa
				bl += w * float32(px[2]) * pa
				a += w * pa
			}
			hrow[x*4], hrow[x*4+1], hrow[x*4+2], hrow[x*4+3] = r, g, bl, a
		}

		pos, end := float64(y), float64(y+1)
		for pos < end {
			o := max(base, min(th-1, int(pos/rowScale)))
			oEnd := float64(o+1) * rowScale
			if o == th-1 {
				oEnd = float64(sh)
			} else if oEnd <= pos {
				o++
				oEnd = float64(o+1) * rowScale
			}
			segEnd := math.Min(end, oEnd)
			w := float32((segEnd - pos) / rowScale)
			dst := acc[o-base]
			for i, v := range hrow {
				dst[i] += v * w
			}
			pos = segEnd
			if pos >= oEnd && o == base {
				flush()
			}
		}
	}
	for base < th {
		flush()
	}
	return out
}

func clampByte(v float32) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}

// readRow writes row y of src (absolute coordinates, sw pixels from x0) into
// dst as straight (non-premultiplied) RGBA.
func readRow(src image.Image, x0, y, sw int, dst []uint8) {
	switch s := src.(type) {
	case *image.NRGBA:
		i := s.PixOffset(x0, y)
		copy(dst, s.Pix[i:i+sw*4])
	case *image.RGBA:
		i := s.PixOffset(x0, y)
		for x := 0; x < sw; x++ {
			r, g, b, a := s.Pix[i], s.Pix[i+1], s.Pix[i+2], s.Pix[i+3]
			if a != 0 && a != 255 {
				r = uint8(uint32(r) * 255 / uint32(a))
				g = uint8(uint32(g) * 255 / uint32(a))
				b = uint8(uint32(b) * 255 / uint32(a))
			}
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = r, g, b, a
			i += 4
		}
	case *image.YCbCr:
		for x := 0; x < sw; x++ {
			yi := s.YOffset(x0+x, y)
			ci := s.COffset(x0+x, y)
			r, g, b := color.YCbCrToRGB(s.Y[yi], s.Cb[ci], s.Cr[ci])
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = r, g, b, 255
		}
	case *image.Gray:
		i := s.PixOffset(x0, y)
		for x := 0; x < sw; x++ {
			v := s.Pix[i+x]
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = v, v, v, 255
		}
	default:
		for x := 0; x < sw; x++ {
			c := color.NRGBAModel.Convert(src.At(x0+x, y)).(color.NRGBA)
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = c.R, c.G, c.B, c.A
		}
	}
}
