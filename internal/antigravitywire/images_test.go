package antigravitywire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"reflect"
	"strings"
	"testing"
)

type imageCapture struct {
	Assets map[string]struct {
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	} `json:"assets"`
	Cases []struct {
		Name   string `json:"name"`
		Exact  bool   `json:"exact"`
		Compat struct {
			MaxImageDimension    int `json:"maxImageDimension"`
			MaxImagePayloadBytes int `json:"maxImagePayloadBytes"`
		} `json:"compat"`
		ImageCount    int `json:"imageCount"`
		MaxDimension  int `json:"maxDimension"`
		PayloadBudget int `json:"payloadBudget"`
		Messages      []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Images []struct {
			Message       int    `json:"message"`
			Block         int    `json:"block"`
			Changed       bool   `json:"changed"`
			MimeType      string `json:"mimeType"`
			Width         int    `json:"width"`
			Height        int    `json:"height"`
			Base64Length  int    `json:"base64Length"`
			HasURL        bool   `json:"hasUrl"`
			HasProviderFi bool   `json:"hasProviderFile"`
		} `json:"images"`
	} `json:"cases"`
}

type captureBlock struct {
	Type         string `json:"type"`
	Asset        string `json:"asset"`
	URL          string `json:"url"`
	ProviderFile any    `json:"providerFile"`
}

// ClampRequestImages must take every decision NeoPi's own
// prepareAnthropicImageContext takes over the same inputs
// (testdata/capture_anthropic_images.ts): which blocks are touched, mime
// choice, resulting dimensions and the base64 budget. Untouched blocks stay
// byte-identical. Cases flagged inexact depend on encoder output sizes (the
// number of shrink passes), so only the bounds are compared there.
func TestClampImagesMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/anthropic_images_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture imageCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Cases) == 0 {
		t.Fatal("empty capture")
	}
	for _, tc := range capture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var images []*Image
			var originals []Image
			for _, m := range tc.Messages {
				if m.Role != "user" && m.Role != "developer" && m.Role != "toolResult" {
					continue
				}
				var blocks []captureBlock
				if json.Unmarshal(m.Content, &blocks) != nil {
					continue // string content carries no image blocks
				}
				for _, b := range blocks {
					if b.Type != "image" {
						continue
					}
					a := capture.Assets[b.Asset]
					img := &Image{Data: a.Data, MimeType: a.MimeType, URL: b.URL, ProviderFile: b.ProviderFile}
					images = append(images, img)
					originals = append(originals, *img)
				}
			}
			if len(images) != len(tc.Images) {
				t.Fatalf("image blocks: got %d want %d", len(images), len(tc.Images))
			}
			if len(images) != tc.ImageCount {
				t.Fatalf("image count: got %d want %d", len(images), tc.ImageCount)
			}
			caps := CapsForCount(len(images), tc.Compat.MaxImageDimension, tc.Compat.MaxImagePayloadBytes)
			if caps.MaxDimension != tc.MaxDimension {
				t.Fatalf("max dimension: got %d want %d", caps.MaxDimension, tc.MaxDimension)
			}
			changed := ClampImages(images, caps)

			wantChanged := 0
			for i, want := range tc.Images {
				got, orig := images[i], originals[i]
				if want.Changed {
					wantChanged++
				}
				isChanged := got.Data != orig.Data || got.MimeType != orig.MimeType
				if isChanged != want.Changed {
					t.Fatalf("image %d: changed=%v want %v (%dx? %s)", i, isChanged, want.Changed, want.Width, want.MimeType)
				}
				if !isChanged {
					if got.URL != orig.URL || !reflect.DeepEqual(got.ProviderFile, orig.ProviderFile) {
						t.Errorf("image %d: untouched block lost its references", i)
					}
					if (orig.URL != "") != want.HasURL {
						t.Errorf("image %d: url presence mismatch with capture", i)
					}
					continue
				}
				if got.URL != "" || got.ProviderFile != nil || want.HasURL || want.HasProviderFi {
					t.Errorf("image %d: rebuilt block must drop url/providerFile (got %q %v, neopi %v %v)", i, got.URL, got.ProviderFile, want.HasURL, want.HasProviderFi)
				}
				raw, err := base64.StdEncoding.DecodeString(got.Data)
				if err != nil {
					t.Fatalf("image %d: output is not base64: %v", i, err)
				}
				cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
				if err != nil {
					t.Fatalf("image %d: output does not decode: %v", i, err)
				}
				if got.MimeType != "image/"+format {
					t.Errorf("image %d: mime %q does not match encoded format %q", i, got.MimeType, format)
				}
				withinBudget := len(got.Data) <= tc.PayloadBudget
				wantWithin := want.Base64Length <= tc.PayloadBudget
				if withinBudget != wantWithin {
					t.Errorf("image %d: base64 length %d within budget %d = %v, neopi %d = %v", i, len(got.Data), tc.PayloadBudget, withinBudget, want.Base64Length, wantWithin)
				}
				if cfg.Width > tc.MaxDimension || cfg.Height > tc.MaxDimension {
					t.Errorf("image %d: %dx%d exceeds cap %d", i, cfg.Width, cfg.Height, tc.MaxDimension)
				}
				if !tc.Exact {
					continue
				}
				if cfg.Width != want.Width || cfg.Height != want.Height {
					t.Errorf("image %d: dimensions %dx%d, neopi %dx%d", i, cfg.Width, cfg.Height, want.Width, want.Height)
				}
				if got.MimeType != want.MimeType {
					t.Errorf("image %d: mime %s, neopi %s", i, got.MimeType, want.MimeType)
				}
			}
			if changed != wantChanged {
				t.Errorf("changed count: got %d want %d", changed, wantChanged)
			}
		})
	}
}

func TestCapsForCount(t *testing.T) {
	cases := []struct {
		count, dim, payload int
		wantDim, wantBytes  int
	}{
		{0, 0, 0, 8000, 10 << 20},
		{20, 0, 0, 8000, 10 << 20},
		{21, 0, 0, 2000, 10 << 20},
		{21, 500, 0, 500, 10 << 20},
		{21, 12000, 4096, 2000, 4096},
		{3, 12000, 4096, 12000, 4096},
		{3, -1, -1, 8000, 10 << 20},
	}
	for _, c := range cases {
		got := CapsForCount(c.count, c.dim, c.payload)
		if got.MaxDimension != c.wantDim || got.MaxPayloadBytes != c.wantBytes {
			t.Errorf("CapsForCount(%d,%d,%d) = %+v, want dim %d bytes %d", c.count, c.dim, c.payload, got, c.wantDim, c.wantBytes)
		}
	}
}

func encodeTestPNG(t *testing.T, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func decodeTestConfig(t *testing.T, b64 string) (image.Config, string) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, format
}

func TestClampImagesGIFFirstFrame(t *testing.T) {
	pal := color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}}
	frame := image.NewPaletted(image.Rect(0, 0, 40, 10), pal)
	for i := range frame.Pix {
		frame.Pix[i] = uint8(i / 4 % 2)
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, frame, nil); err != nil {
		t.Fatal(err)
	}
	img := &Image{Data: base64.StdEncoding.EncodeToString(buf.Bytes()), MimeType: "image/gif", URL: "https://x/y.gif"}
	if n := ClampImages([]*Image{img}, ImageCaps{MaxDimension: 20}); n != 1 {
		t.Fatalf("changed = %d, want 1", n)
	}
	cfg, format := decodeTestConfig(t, img.Data)
	if cfg.Width != 20 || cfg.Height != 5 {
		t.Errorf("got %dx%d, want 20x5", cfg.Width, cfg.Height)
	}
	if img.MimeType != "image/"+format || img.URL != "" {
		t.Errorf("mime %q format %q url %q", img.MimeType, format, img.URL)
	}
}

// An image inside the pixel cap is only replaced if the re-encode shrank it.
func TestClampImagesKeepsOriginalWhenNotShrunk(t *testing.T) {
	g := image.NewGray(image.Rect(0, 0, 1, 1))
	data := encodeTestPNG(t, g)
	img := &Image{Data: data, MimeType: "image/png"}
	// Budget below the payload forces a re-encode; the RGB re-encode of a
	// 1x1 grey PNG is not smaller, so the original stays.
	if n := ClampImages([]*Image{img}, ImageCaps{MaxPayloadBytes: 10}); n != 0 {
		t.Fatalf("changed = %d, want 0", n)
	}
	if img.Data != data || img.MimeType != "image/png" {
		t.Error("image was modified")
	}
}

func TestClampImagesUndecodableStaysIdentical(t *testing.T) {
	for name, data := range map[string]string{
		"empty":   "",
		"garbage": base64.StdEncoding.EncodeToString([]byte("not an image")),
		"webp":    base64.StdEncoding.EncodeToString([]byte("RIFF\x24\x00\x00\x00WEBPVP8 ")),
		"base64":  "%%%%",
	} {
		img := &Image{Data: data, MimeType: "image/webp", URL: "u"}
		if n := ClampImages([]*Image{nil, img}, ImageCaps{MaxDimension: 1, MaxPayloadBytes: 1}); n != 0 {
			t.Errorf("%s: changed = %d", name, n)
		}
		if img.Data != data || img.URL != "u" {
			t.Errorf("%s: modified", name)
		}
	}
}

func TestDecodeBase64Lenient(t *testing.T) {
	want := []byte("hello, image clamp?>~")
	std := base64.StdEncoding.EncodeToString(want)
	urlSafe := base64.RawURLEncoding.EncodeToString(want)
	wrapped := std[:8] + "\r\n" + std[8:]
	for name, in := range map[string]string{"std": std, "urlsafe": urlSafe, "wrapped": wrapped, "padded-garbage": std + "=====trailing"} {
		if got := decodeBase64Lenient(in); !bytes.HasPrefix(got, want) {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

// Area averaging: a one-pixel checkerboard halved is a uniform mid grey, and
// fractional coverage splits an odd axis proportionally.
func TestDownscaleAreaAverage(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			v := uint8(0)
			if (x+y)%2 == 0 {
				v = 255
			}
			src.SetNRGBA(x, y, color.NRGBA{v, v, v, 255})
		}
	}
	out := downscale(src, 4, 4)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if c := out.NRGBAAt(x, y); c.R < 127 || c.R > 128 || c.A != 255 {
				t.Fatalf("(%d,%d) = %v, want mid grey", x, y, c)
			}
		}
	}

	strip := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	strip.SetNRGBA(0, 0, color.NRGBA{0, 0, 0, 255})
	strip.SetNRGBA(1, 0, color.NRGBA{255, 255, 255, 255})
	strip.SetNRGBA(2, 0, color.NRGBA{255, 255, 255, 255})
	two := downscale(strip, 2, 1)
	// Output pixel 0 covers 1.5 source pixels: 1.0 black + 0.5 white.
	if c := two.NRGBAAt(0, 0); c.R < 84 || c.R > 86 {
		t.Errorf("fractional coverage: got %v, want ~85", c)
	}
	if c := two.NRGBAAt(1, 0); c.R != 255 {
		t.Errorf("tail pixel: got %v, want white", c)
	}
}

func TestDownscaleTransparentDoesNotBleed(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255})
	src.SetNRGBA(1, 0, color.NRGBA{0, 255, 0, 0})
	c := downscale(src, 1, 1).NRGBAAt(0, 0)
	if c.R != 255 || c.G != 0 || c.A < 127 || c.A > 128 {
		t.Errorf("got %v, want opaque-weighted red at half alpha", c)
	}
}

func TestClampImagesPayloadShrinkPasses(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 96, 96))
	seed := uint32(1)
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = uint8(seed >> 24)
		if i%4 == 3 {
			img.Pix[i] = 255
		}
	}
	data := encodeTestPNG(t, img)
	block := &Image{Data: data, MimeType: "image/png"}
	caps := ImageCaps{MaxPayloadBytes: 2500}
	if n := ClampImages([]*Image{block}, caps); n != 1 {
		t.Fatalf("changed = %d, want 1", n)
	}
	if len(block.Data) > 2250 {
		t.Errorf("base64 length %d over budget 2250", len(block.Data))
	}
	cfg, _ := decodeTestConfig(t, block.Data)
	if cfg.Width >= 96 || cfg.Height >= 96 || cfg.Width != cfg.Height {
		t.Errorf("expected a uniform downscale below 96x96, got %dx%d", cfg.Width, cfg.Height)
	}
	if !strings.HasPrefix(block.MimeType, "image/") {
		t.Errorf("mime %q", block.MimeType)
	}
}
