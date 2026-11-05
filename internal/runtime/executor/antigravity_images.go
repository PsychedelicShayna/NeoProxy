package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antigravitywire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiretry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NeoPi runs Antigravity image models through generateAntigravityImage
// (pi-ai images/google-antigravity.ts). The proxy answers the OpenAI image
// endpoints with that same upstream exchange, ported in
// antigravitywire.GenerateImage; this file maps the Images API request onto
// NeoPi's ImageGenerationRequest and its result back onto the Images API.

// isAntigravityImageRequest reports whether the request arrived on an OpenAI
// image endpoint rather than as a chat turn.
func isAntigravityImageRequest(opts cliproxyexecutor.Options) bool {
	return isCodexOpenAIImageRequest(opts)
}

// antigravityImageCall is one Images API request shaped for NeoPi.
type antigravityImageCall struct {
	request        antigravitywire.ImageRequest
	responseFormat string
	streamPrefix   string
	// original is the JSON the client sent, or nil for a multipart edit; the
	// payload rules match against it.
	original []byte
}

// antigravityImageAspectRatios are the aspect ratios Gemini image models take.
var antigravityImageAspectRatios = []struct {
	label string
	ratio float64
}{
	{"1:1", 1}, {"2:3", 2.0 / 3}, {"3:2", 3.0 / 2}, {"3:4", 3.0 / 4}, {"4:3", 4.0 / 3},
	{"4:5", 4.0 / 5}, {"5:4", 5.0 / 4}, {"9:16", 9.0 / 16}, {"16:9", 16.0 / 9}, {"21:9", 21.0 / 9},
}

// antigravityAspectRatioFromSize maps an OpenAI `WIDTHxHEIGHT` size onto the
// nearest supported aspect ratio; NeoPi's callers set the aspect ratio
// directly, so this is the proxy's only translation.
func antigravityAspectRatioFromSize(size string) string {
	width, height, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		return ""
	}
	var w, h float64
	if _, err := fmt.Sscanf(strings.TrimSpace(width), "%f", &w); err != nil || w <= 0 {
		return ""
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(height), "%f", &h); err != nil || h <= 0 {
		return ""
	}
	target := math.Log(w / h)
	best, distance := "", math.Inf(1)
	for _, candidate := range antigravityImageAspectRatios {
		if d := math.Abs(math.Log(candidate.ratio) - target); d < distance {
			best, distance = candidate.label, d
		}
	}
	return best
}

func antigravityDataURLImage(raw string) (antigravitywire.ImageInput, error) {
	raw = strings.TrimSpace(raw)
	header, data, ok := strings.Cut(raw, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return antigravitywire.ImageInput{}, statusErr{code: http.StatusBadRequest,
			msg: "Invalid request: images must be base64 data URLs"}
	}
	return antigravitywire.ImageInput{MimeType: strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"), Data: data}, nil
}

// antigravityImageOptions reads n, size, aspect_ratio, image_size and
// response_format from a JSON body or a multipart form (value is the field reader).
func antigravityImageOptions(value func(string) string, call *antigravityImageCall) {
	if n := strings.TrimSpace(value("n")); n != "" {
		var count int
		if _, err := fmt.Sscanf(n, "%d", &count); err == nil && count > 0 {
			call.request.Count = &count
		}
	}
	call.request.AspectRatio = strings.TrimSpace(value("aspect_ratio"))
	if call.request.AspectRatio == "" {
		call.request.AspectRatio = strings.TrimSpace(value("aspectRatio"))
	}
	if call.request.AspectRatio == "" {
		call.request.AspectRatio = antigravityAspectRatioFromSize(value("size"))
	}
	call.request.ImageSize = strings.TrimSpace(value("image_size"))
	if call.request.ImageSize == "" {
		call.request.ImageSize = strings.TrimSpace(value("imageSize"))
	}
	if strings.EqualFold(strings.TrimSpace(value("response_format")), "url") {
		call.responseFormat = "url"
	}
}

// antigravityImageJSONInputs collects the data-URL images of a JSON body:
// `images[].image_url` and `image` (a data URL or a list of them).
func antigravityImageJSONInputs(body gjson.Result) ([]antigravitywire.ImageInput, error) {
	var urls []string
	body.Get("images").ForEach(func(_, entry gjson.Result) bool {
		if entry.Type == gjson.String {
			urls = append(urls, entry.String())
		} else if url := entry.Get("image_url"); url.Type == gjson.String {
			urls = append(urls, url.String())
		} else if url := entry.Get("image_url.url"); url.Type == gjson.String {
			urls = append(urls, url.String())
		}
		return true
	})
	switch image := body.Get("image"); {
	case image.Type == gjson.String:
		urls = append(urls, image.String())
	case image.IsArray():
		for _, entry := range image.Array() {
			if entry.Type == gjson.String {
				urls = append(urls, entry.String())
			}
		}
	}
	inputs := make([]antigravitywire.ImageInput, 0, len(urls))
	for _, url := range urls {
		input, err := antigravityDataURLImage(url)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

// antigravityPrepareImageCall shapes an Images API request as NeoPi's
// ImageGenerationRequest.
func antigravityPrepareImageCall(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (antigravityImageCall, error) {
	call := antigravityImageCall{responseFormat: "b64_json", streamPrefix: "image_generation"}
	path := helps.PayloadRequestPath(opts)
	edit := strings.HasSuffix(path, codexImagesEditsPath)
	if edit {
		call.streamPrefix = "image_edit"
	}
	mediaType, params, _ := mime.ParseMediaType(codexImageContentType(opts.Headers))
	if edit && strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		return call, antigravityPrepareMultipartImageCall(req.Payload, params["boundary"], &call)
	}
	if !gjson.ValidBytes(req.Payload) {
		return call, statusErr{code: http.StatusBadRequest, msg: "Invalid request: body must be valid JSON"}
	}
	body := gjson.ParseBytes(req.Payload)
	call.original = req.Payload
	call.request.Prompt = body.Get("prompt").String()
	antigravityImageOptions(func(key string) string { return body.Get(key).String() }, &call)
	inputs, err := antigravityImageJSONInputs(body)
	if err != nil {
		return call, err
	}
	call.request.InputImages = inputs
	return call, antigravityCheckImageCall(call, edit)
}

func antigravityPrepareMultipartImageCall(payload []byte, boundary string, call *antigravityImageCall) error {
	if boundary == "" {
		return statusErr{code: http.StatusBadRequest, msg: "Invalid request: multipart boundary is missing"}
	}
	values := map[string]string{}
	reader := multipart.NewReader(bytes.NewReader(payload), boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("Invalid request: %v", err)}
		}
		data, errRead := io.ReadAll(part)
		_ = part.Close()
		if errRead != nil {
			return statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("Invalid request: %v", errRead)}
		}
		name := part.FormName()
		if part.FileName() == "" {
			if _, seen := values[name]; !seen {
				values[name] = string(data)
			}
			continue
		}
		if name != "image" && name != "image[]" {
			continue
		}
		mediaType := strings.TrimSpace(part.Header.Get("Content-Type"))
		if mediaType == "" {
			mediaType = http.DetectContentType(data)
		}
		call.request.InputImages = append(call.request.InputImages, antigravitywire.ImageInput{
			Data: base64.StdEncoding.EncodeToString(data), MimeType: mediaType,
		})
	}
	call.request.Prompt = values["prompt"]
	antigravityImageOptions(func(key string) string { return values[key] }, call)
	return antigravityCheckImageCall(*call, true)
}

func antigravityCheckImageCall(call antigravityImageCall, edit bool) error {
	if strings.TrimSpace(call.request.Prompt) == "" {
		return statusErr{code: http.StatusBadRequest, msg: "Invalid request: prompt is required"}
	}
	if edit && len(call.request.InputImages) == 0 {
		return statusErr{code: http.StatusBadRequest, msg: "Invalid request: image is required"}
	}
	return nil
}

// antigravityImageModel resolves the NeoPi record of the image model the
// request was routed to: the account's discovered row, else NeoPi's bundled one.
func antigravityImageModel(req cliproxyexecutor.Request, modelID string) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.API == "google-gemini-cli" {
		return info.NeoPi
	}
	return registry.AntigravityBundledImageNeoPiModel(modelID)
}

// antigravityPreparedImage is one Images API request ready to send.
type antigravityPreparedImage struct {
	model  *registry.NeoPiModel
	params antigravitywire.ImageParams
	call   antigravityImageCall
}

func (e *AntigravityExecutor) prepareImage(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*antigravityPreparedImage, error) {
	credential := antigravityCredentialOf(auth)
	if credential.accessToken == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "antigravity: account has no access token"}
	}
	if credential.projectID == "" {
		return nil, statusErr{code: http.StatusBadRequest,
			msg: "Antigravity image credentials must contain token and projectId"}
	}
	if credential.hasExpiry && !time.Now().Before(credential.expiresAt) {
		return nil, statusErr{code: http.StatusUnauthorized,
			msg: "OAuth token expired before request — please retry; AuthStorage will refresh on the next attempt."}
	}
	modelID := thinking.ParseSuffix(req.Model).ModelName
	model := antigravityImageModel(req, modelID)
	if model == nil {
		return nil, statusErr{code: http.StatusBadRequest,
			msg: fmt.Sprintf("antigravity: model %s is not an image model in the account's Antigravity catalog", modelID)}
	}
	call, err := antigravityPrepareImageCall(req, opts)
	if err != nil {
		return nil, err
	}
	baseURL := credential.baseURL
	if baseURL == "" {
		baseURL = model.BaseURL
	}
	if baseURL == "" {
		baseURL = antigravitywire.DailyEndpoint
	}
	payloadOpts := opts
	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), model.ID, "antigravity", "", call.original, req, payloadOpts)
	return &antigravityPreparedImage{
		model: model,
		call:  call,
		params: antigravitywire.ImageParams{
			ModelID:        model.ID,
			RequestModelID: model.RequestModelID,
			BaseURL:        baseURL,
			Token:          credential.accessToken,
			ProjectID:      credential.projectID,
			Finalize:       finalize,
		},
	}, nil
}

// generate runs NeoPi's generateAntigravityImage against the account.
func (p *antigravityPreparedImage) generate(ctx context.Context, client *http.Client) (*antigravitywire.ImageResult, error) {
	result, err := antigravitywire.GenerateImage(ctx, client, p.params, p.call.request)
	if err != nil {
		return nil, antigravityImageError(err)
	}
	return result, nil
}

// antigravityImageError maps NeoPi's image errors onto the statuses the
// proxy's scheduler reads.
func antigravityImageError(err error) error {
	var invalid *antigravitywire.ImageValidationError
	if errors.As(err, &invalid) {
		return statusErr{code: http.StatusBadRequest, msg: "Invalid request: " + invalid.Message}
	}
	var api *antigravitywire.ImageAPIError
	if !errors.As(err, &api) {
		return err
	}
	out := statusErr{code: api.Status, msg: api.Message}
	if api.Status == http.StatusTooManyRequests {
		out.credentialScoped = true
		delay := 30 * time.Second
		if hint, ok := neopiretry.ExtractRetryHint(api.Header, api.Message, time.Now()); ok && hint > 0 {
			delay = time.Duration(hint * float64(time.Millisecond))
		}
		out.retryAfter = &delay
	}
	return out
}

// antigravityImageUsageDetail is the answer's usage as the proxy records it.
func antigravityImageUsageDetail(usage antigravitywire.ImageUsage) coreusage.Detail {
	total := usage.Input + usage.Output
	return coreusage.Detail{InputTokens: int64(usage.Input), OutputTokens: int64(usage.Output), TotalTokens: int64(total)}
}

// antigravitySetImageUsage writes the answer's usage as the Images API reports it.
func antigravitySetImageUsage(doc []byte, usage antigravitywire.ImageUsage) []byte {
	doc, _ = sjson.SetBytes(doc, "usage.input_tokens", usage.Input)
	doc, _ = sjson.SetBytes(doc, "usage.output_tokens", usage.Output)
	doc, _ = sjson.SetBytes(doc, "usage.total_tokens", usage.Input+usage.Output)
	return doc
}

func antigravityImageOutputFormat(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return "jpeg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	}
	return "png"
}

func antigravityImageItem(image antigravitywire.GeneratedImage, responseFormat string) []byte {
	item := []byte(`{}`)
	if responseFormat == "url" {
		item, _ = sjson.SetBytes(item, "url", "data:"+image.MimeType+";base64,"+image.Data)
	} else {
		item, _ = sjson.SetBytes(item, "b64_json", image.Data)
	}
	return item
}

// antigravityImagesResponse is the Images API body of one generation.
func antigravityImagesResponse(result *antigravitywire.ImageResult, responseFormat string, now time.Time) []byte {
	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", now.Unix())
	if len(result.Images) != 0 {
		out, _ = sjson.SetBytes(out, "output_format", antigravityImageOutputFormat(result.Images[0].MimeType))
	}
	out = antigravitySetImageUsage(out, result.Usage)
	items := make([][]byte, 0, len(result.Images))
	for _, image := range result.Images {
		items = append(items, antigravityImageItem(image, responseFormat))
	}
	out, _ = sjson.SetRawBytes(out, "data", helps.JoinRawJSONArray(items))
	return out
}

// antigravityImageCompletedFrame is one `<prefix>.completed` stream event.
func antigravityImageCompletedFrame(image antigravitywire.GeneratedImage, usage antigravitywire.ImageUsage, responseFormat, streamPrefix string) []byte {
	eventName := streamPrefix + ".completed"
	data := []byte(`{"type":""}`)
	data, _ = sjson.SetBytes(data, "type", eventName)
	data = antigravitySetImageUsage(data, usage)
	if responseFormat == "url" {
		data, _ = sjson.SetBytes(data, "url", "data:"+image.MimeType+";base64,"+image.Data)
	} else {
		data, _ = sjson.SetBytes(data, "b64_json", image.Data)
	}
	return codexBuildSSEFrame(eventName, data)
}

// executeImage serves a non-streaming Images API request.
func (e *AntigravityExecutor) executeImage(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	prepared, err := e.prepareImage(ctx, auth, req, opts)
	if err != nil {
		return resp, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	result, err := prepared.generate(ctx, client)
	if err != nil {
		return resp, err
	}
	detail := antigravityImageUsageDetail(result.Usage)
	reporter.SetUpstreamModel(prepared.model.ID)
	reporter.Publish(ctx, detail)
	out := antigravityImagesResponse(result, prepared.call.responseFormat, time.Now())
	helps.AppendAPIResponseChunk(ctx, e.cfg, out)
	return cliproxyexecutor.Response{Payload: out, Headers: http.Header{}}, nil
}

// executeImageStream serves a streaming Images API request. NeoPi's image
// client is not incremental: the upstream stream is collected whole, then each
// image is emitted as a `<prefix>.completed` event.
func (e *AntigravityExecutor) executeImageStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	prepared, err := e.prepareImage(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		result, errGenerate := prepared.generate(ctx, client)
		if errGenerate != nil {
			reporter.PublishFailure(ctx, errGenerate)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errGenerate}:
			case <-ctx.Done():
			}
			return
		}
		detail := antigravityImageUsageDetail(result.Usage)
		reporter.SetUpstreamModel(prepared.model.ID)
		reporter.Publish(ctx, detail)
		for _, image := range result.Images {
			frame := antigravityImageCompletedFrame(image, result.Usage, prepared.call.responseFormat, prepared.call.streamPrefix)
			helps.AppendAPIResponseChunk(ctx, e.cfg, frame)
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: frame}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: out}, nil
}
