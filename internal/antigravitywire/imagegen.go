package antigravitywire

// Go port of NeoPi's Antigravity image generation (pi-ai images/google-antigravity.ts
// generateAntigravityImage) with catalog discovery's fetchAntigravityImageModel:
// the account's advertised image model is resolved with fetchAvailableModels,
// then one Cloud Code Assist `streamGenerateContent?alt=sse` request is sent
// through the endpoint ladder and its SSE chunks are collected into images,
// text and usage.
//
// Auth retry (NeoPi's withAuth) belongs to the proxy's credential manager, and
// the structured credential NeoPi's key carries ({token, projectId}) arrives as
// the two fields of ImageParams.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

const (
	// ImageProvider is the catalog provider id NeoPi names in image errors.
	ImageProvider = "google-antigravity"

	// imageSystemInstruction is IMAGE_SYSTEM_INSTRUCTION.
	imageSystemInstruction = "You are an AI image generator. Generate images based on user descriptions. Focus on creating high-quality, visually appealing images that match the user's request."

	streamGeneratePath = "/v1internal:streamGenerateContent?alt=sse"

	// imageSafetySettings is the fixed safetySettings array NeoPi sends.
	imageSafetySettings = `[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_ONLY_HIGH"},{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_ONLY_HIGH"},{"category":"HARM_CATEGORY_SEXUALLY_EXPLICIT","threshold":"BLOCK_ONLY_HIGH"},{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","threshold":"BLOCK_ONLY_HIGH"},{"category":"HARM_CATEGORY_CIVIC_INTEGRITY","threshold":"BLOCK_ONLY_HIGH"}]`
)

// ImageInput is one reference image (NeoPi's ImageInput): base64 data and its
// MIME type.
type ImageInput struct {
	Data     string
	MimeType string
}

// ImageRequest is NeoPi's ImageGenerationRequest. Empty AspectRatio and
// ImageSize are NeoPi's undefined; a nil Count sends 1.
type ImageRequest struct {
	Prompt      string
	InputImages []ImageInput
	AspectRatio string
	ImageSize   string
	Count       *int
}

// GeneratedImage is one image of the answer.
type GeneratedImage struct {
	Data     string
	MimeType string
}

// ImageUsage is usageFromWire over the last usageMetadata chunk. The counts
// are JavaScript numbers, so they may be fractional.
type ImageUsage struct {
	Input  float64
	Output float64
}

// ImageResult is NeoPi's ImageGenerationResult.
type ImageResult struct {
	Images []GeneratedImage
	// Text is the model's commentary, the text parts joined by a space.
	Text  string
	Usage ImageUsage
}

// ImageValidationError is NeoPi's ValidationError for an unusable input image.
type ImageValidationError struct{ Message string }

func (e *ImageValidationError) Error() string { return e.Message }

// ImageAPIError is NeoPi's ImageApiError: an upstream refusal with its status
// and response headers.
type ImageAPIError struct {
	Status  int
	Message string
	Header  http.Header
}

func (e *ImageAPIError) Error() string { return e.Message }

// ImageParams is the account and catalog model one generation runs against.
type ImageParams struct {
	// ModelID is the catalog model id, named in errors and sent when the
	// account advertises no image model and the row has no RequestModelID.
	ModelID string
	// RequestModelID is the row's requestModelId.
	RequestModelID string
	// BaseURL is the row's base URL, tried before the default hosts.
	BaseURL string
	// Token and ProjectID are NeoPi's structured credential.
	Token     string
	ProjectID string
	// RequestID, when set, replaces the generated requestId (tests).
	RequestID string
	// Now is the clock behind the generated requestId; nil is time.Now.
	Now func() time.Time
	// Finalize, when set, is the proxy's payload-rule barrier: it runs on the
	// finished envelope immediately before the request is sent.
	Finalize func(body []byte) []byte
}

var (
	base64ImageData = regexp.MustCompile(`^([A-Za-z0-9+/]+)(={0,2})$`)
	imageMimeType   = regexp.MustCompile(`(?i)^image/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}$`)
)

const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// ValidateImageInput is validateImageInput: non-empty standard base64 with
// canonical padding and trailing bits, and an RFC 6838 image MIME type.
func ValidateImageInput(image ImageInput) error {
	if image.Data == "" {
		return &ImageValidationError{Message: "empty image data"}
	}
	match := base64ImageData.FindStringSubmatch(image.Data)
	if match == nil {
		return &ImageValidationError{Message: "invalid base64 image data"}
	}
	encoded, padding := match[1], len(match[2])
	remainder := len(encoded) % 4
	if remainder == 1 || (padding == 1 && remainder != 3) || (padding == 2 && remainder != 2) {
		return &ImageValidationError{Message: "invalid base64 image data"}
	}
	last := strings.IndexByte(base64Alphabet, encoded[len(encoded)-1])
	if (remainder == 2 && last&0b1111 != 0) || (remainder == 3 && last&0b11 != 0) {
		return &ImageValidationError{Message: "invalid base64 image data"}
	}
	if !imageMimeType.MatchString(image.MimeType) {
		return &ImageValidationError{Message: "invalid image MIME type"}
	}
	return nil
}

// ImageEndpoints is antigravityEndpoints: the row's base URL, then the daily
// and sandbox hosts, without repeats.
func ImageEndpoints(baseURL string) []string {
	out := make([]string, 0, 3)
	for _, endpoint := range []string{strings.TrimRight(baseURL, "/"), DailyEndpoint, SandboxEndpoint} {
		duplicate := false
		for _, seen := range out {
			if seen == endpoint {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, endpoint)
		}
	}
	return out
}

// ImageTarget is NeoPi's AntigravityTarget: the wire model id and the ordered
// endpoints.
type ImageTarget struct {
	Model     string
	Endpoints []string
}

// ResolveImageTarget is resolveTarget: the image model the account
// advertises (and the endpoint that served the roster) when fetchAvailableModels
// names one, else the row's own wire id on the default endpoint list.
func ResolveImageTarget(ctx context.Context, client *http.Client, params ImageParams) ImageTarget {
	endpoints := ImageEndpoints(params.BaseURL)
	if id, endpoint, ok := FetchImageModel(ctx, client, params.Token, params.ProjectID); ok {
		ordered := []string{endpoint}
		for _, candidate := range endpoints {
			if candidate != endpoint {
				ordered = append(ordered, candidate)
			}
		}
		return ImageTarget{Model: id, Endpoints: ordered}
	}
	model := params.RequestModelID
	if model == "" {
		model = params.ModelID
	}
	return ImageTarget{Model: model, Endpoints: endpoints}
}

// FetchImageModel is fetchAntigravityImageModel: the first image-generation
// model id the account advertises and the endpoint that answered. A rejected
// credential, a transient failure and an invalid payload all report none.
func FetchImageModel(ctx context.Context, client *http.Client, token, projectID string) (id, endpoint string, ok bool) {
	if client == nil {
		client = http.DefaultClient
	}
	// fetchAntigravityDiscoveryResponse resolves the latest client version
	// whenever no user agent is overridden.
	EnsureVersion(ctx, client)
	body := jsjson.Object()
	if projectID != "" {
		body = jsjson.Object(jsjson.Field{Name: "project", Value: jsjson.Marshal(projectID)})
	}
	for _, candidate := range []string{DailyEndpoint, SandboxEndpoint} {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, candidate+"/v1internal:fetchAvailableModels", bytes.NewReader(body))
		if err != nil {
			continue
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		SetClientHeaders(request.Header, "")
		response, err := client.Do(request)
		if err != nil {
			continue
		}
		text, errRead := readImageBody(response)
		_ = response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 || errRead != nil {
			continue
		}
		// Bun's response.json() yields null for an empty body and rejects other
		// non-JSON text; neither validates as a roster.
		if text == "" || !gjson.Valid(text) {
			continue
		}
		payload := gjson.Parse(text)
		// The roster schema declares every key optional, so an object or an array
		// validates; any other root is an invalid response.
		if !payload.IsObject() && !payload.IsArray() {
			continue
		}
		id = firstImageModelID(payload)
		return id, candidate, id != ""
	}
	return "", "", false
}

// firstImageModelID is `imageGenerationModelIds?.find(id => id.length > 0)`
// over the schema's string-only filter.
func firstImageModelID(payload gjson.Result) string {
	if !payload.IsObject() {
		return ""
	}
	var ids gjson.Result
	payload.ForEach(func(key, value gjson.Result) bool {
		if key.String() == "imageGenerationModelIds" {
			ids = value // a repeated key keeps its last value, as JSON.parse does
		}
		return true
	})
	if !ids.IsArray() {
		return ""
	}
	for _, entry := range ids.Array() {
		if entry.Type == gjson.String && entry.Str != "" {
			return entry.Str
		}
	}
	return ""
}

func readImageBody(response *http.Response) (string, error) {
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return "", err
	}
	defer func() { _ = decoded.Close() }()
	data, err := io.ReadAll(decoded)
	return string(data), err
}

// ImageRequestID is `agent-${Date.now()}-${Math.random().toString(36).slice(2, 11)}`.
func ImageRequestID(now time.Time) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	suffix := make([]byte, 9)
	for i := range suffix {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			n = big.NewInt(int64(now.Nanosecond()+i) % int64(len(alphabet)))
		}
		suffix[i] = alphabet[n.Int64()]
	}
	return "agent-" + strconv.FormatInt(now.UnixMilli(), 10) + "-" + string(suffix)
}

// BuildImageRequestBody is buildRequest: the Cloud Code Assist envelope, byte
// for byte what JSON.stringify gives NeoPi's object.
func BuildImageRequestBody(request ImageRequest, model, projectID, requestID string) []byte {
	parts := make([]json.RawMessage, 0, len(request.InputImages)+1)
	for _, image := range request.InputImages {
		parts = append(parts, jsjson.Object(jsjson.Field{Name: "inlineData", Value: jsjson.Object(
			jsjson.Field{Name: "data", Value: jsjson.Marshal(image.Data)},
			jsjson.Field{Name: "mimeType", Value: jsjson.Marshal(image.MimeType)},
		)}))
	}
	parts = append(parts, jsjson.Object(jsjson.Field{Name: "text", Value: jsjson.Marshal(request.Prompt)}))

	generation := []jsjson.Field{{Name: "responseModalities", Value: jsjson.Marshal([]string{"IMAGE"})}}
	if request.AspectRatio != "" || request.ImageSize != "" {
		config := jsjson.Object(
			jsjson.Field{Name: "aspectRatio", Value: optionalString(request.AspectRatio)},
			jsjson.Field{Name: "imageSize", Value: optionalString(request.ImageSize)},
		)
		generation = append(generation, jsjson.Field{Name: "imageConfig", Value: config})
	}
	count := 1
	if request.Count != nil {
		count = *request.Count
	}
	generation = append(generation, jsjson.Field{Name: "candidateCount", Value: jsjson.Marshal(count)})

	return jsjson.Object(
		jsjson.Field{Name: "project", Value: jsjson.Marshal(projectID)},
		jsjson.Field{Name: "model", Value: jsjson.Marshal(model)},
		jsjson.Field{Name: "request", Value: jsjson.Object(
			jsjson.Field{Name: "contents", Value: imageJSONArray(jsjson.Object(
				jsjson.Field{Name: "role", Value: jsjson.Marshal("user")},
				jsjson.Field{Name: "parts", Value: imageJSONArray(parts...)},
			))},
			jsjson.Field{Name: "systemInstruction", Value: jsjson.Object(
				jsjson.Field{Name: "parts", Value: imageJSONArray(jsjson.Object(jsjson.Field{Name: "text", Value: jsjson.Marshal(imageSystemInstruction)}))},
			)},
			jsjson.Field{Name: "generationConfig", Value: jsjson.Object(generation...)},
			jsjson.Field{Name: "safetySettings", Value: json.RawMessage(imageSafetySettings)},
		)},
		jsjson.Field{Name: "requestId", Value: jsjson.Marshal(requestID)},
		jsjson.Field{Name: "userAgent", Value: jsjson.Marshal("antigravity")},
	)
}

func optionalString(value string) json.RawMessage {
	if value == "" {
		return nil
	}
	return jsjson.Marshal(value)
}

func imageJSONArray(items ...json.RawMessage) json.RawMessage {
	out := []byte{'['}
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

// ImageErrorMessage is errorMessage: the body's `detail`, else its
// `error.message`, else the raw text.
func ImageErrorMessage(raw string) string {
	if !gjson.Valid(raw) {
		return raw
	}
	parsed := gjson.Parse(raw)
	switch {
	case parsed.Type == gjson.Null:
		// `null.detail` throws and the catch returns the raw text.
		return raw
	case parsed.IsObject():
		if detail := lastMember(parsed, "detail"); detail.Exists() && detail.Type != gjson.Null {
			return jsString(detail)
		}
		if failure := lastMember(parsed, "error"); failure.IsObject() {
			if message := lastMember(failure, "message"); message.Exists() && message.Type != gjson.Null {
				return jsString(message)
			}
		}
	}
	return raw
}

// lastMember is JSON.parse's property read: a repeated key keeps its last value.
func lastMember(object gjson.Result, name string) gjson.Result {
	var found gjson.Result
	object.ForEach(func(key, value gjson.Result) bool {
		if key.String() == name {
			found = value
		}
		return true
	})
	return found
}

// jsString is the String(value) a template literal applies.
func jsString(value gjson.Result) string {
	switch value.Type {
	case gjson.String:
		return value.Str
	case gjson.Number:
		return jsjson.Number(value.Float())
	case gjson.True:
		return "true"
	case gjson.False:
		return "false"
	}
	if value.IsArray() {
		items := value.Array()
		parts := make([]string, len(items))
		for i, item := range items {
			if item.Type != gjson.Null {
				parts[i] = jsString(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// GenerateImage is generateAntigravityImage: it validates the reference
// images, resolves the target and tries each endpoint until one answers 2xx,
// then collects the SSE stream. A transport failure is not retried on the next
// endpoint, exactly as in NeoPi; only 429 and 5xx answers are.
func GenerateImage(ctx context.Context, client *http.Client, params ImageParams, request ImageRequest) (*ImageResult, error) {
	for _, image := range request.InputImages {
		if err := ValidateImageInput(image); err != nil {
			return nil, err
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	target := ResolveImageTarget(ctx, client, params)
	requestID := params.RequestID
	if requestID == "" {
		now := time.Now
		if params.Now != nil {
			now = params.Now
		}
		requestID = ImageRequestID(now())
	}
	body := BuildImageRequestBody(request, target.Model, params.ProjectID, requestID)
	if params.Finalize != nil {
		body = params.Finalize(body)
	}

	var lastError *ImageAPIError
	for index, endpoint := range target.Endpoints {
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+streamGeneratePath, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpRequest.Header.Set("Authorization", "Bearer "+params.Token)
		httpRequest.Header.Set("Content-Type", "application/json")
		SetClientHeaders(httpRequest.Header, "")
		response, err := client.Do(httpRequest)
		if err != nil {
			return nil, err
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return readImageStream(response)
		}
		text, _ := readImageBody(response)
		_ = response.Body.Close()
		lastError = &ImageAPIError{
			Status:  response.StatusCode,
			Message: fmt.Sprintf("%s/%s image request failed (%d): %s", ImageProvider, params.ModelID, response.StatusCode, ImageErrorMessage(text)),
			Header:  response.Header.Clone(),
		}
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		if !retryable || index == len(target.Endpoints)-1 {
			return nil, lastError
		}
	}
	if lastError != nil {
		return nil, lastError
	}
	return nil, errors.New("antigravity image request failed")
}

// readImageStream is parseSse over one 2xx response.
func readImageStream(response *http.Response) (*ImageResult, error) {
	defer func() { _ = response.Body.Close() }()
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = decoded.Close() }()
	return CollectImageStream(decoded)
}

// CollectImageStream reads Cloud Code Assist SSE chunks into images, text and
// usage. The last chunk carrying usageMetadata wins.
func CollectImageStream(body io.Reader) (*ImageResult, error) {
	result := &ImageResult{Images: []GeneratedImage{}}
	var texts []string
	reader := NewSSEReader(body)
	for {
		event, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		chunk := gjson.ParseBytes(event)
		for _, candidate := range chunk.Get("response.candidates").Array() {
			for _, part := range candidate.Get("content.parts").Array() {
				if text := part.Get("text"); text.Type == gjson.String && text.Str != "" {
					texts = append(texts, text.Str)
				}
				data, mimeType := part.Get("inlineData.data"), part.Get("inlineData.mimeType")
				if data.Type == gjson.String && data.Str != "" && mimeType.Type == gjson.String && mimeType.Str != "" {
					result.Images = append(result.Images, GeneratedImage{Data: data.Str, MimeType: mimeType.Str})
				}
			}
		}
		if metadata := chunk.Get("response.usageMetadata"); truthy(metadata) {
			result.Usage = ImageUsage{Input: usageNumber(metadata.Get("promptTokenCount")), Output: usageNumber(metadata.Get("candidatesTokenCount"))}
		}
	}
	result.Text = strings.TrimSpace(strings.Join(texts, " "))
	return result, nil
}

// truthy is JavaScript truthiness of a parsed JSON value.
func truthy(value gjson.Result) bool {
	switch value.Type {
	case gjson.Null:
		return false
	case gjson.False:
		return false
	case gjson.Number:
		return value.Num != 0
	case gjson.String:
		return value.Str != ""
	}
	return value.Exists()
}

// usageNumber is the `typeof usage[key] === "number"` read of usageFromWire.
func usageNumber(value gjson.Result) float64 {
	if value.Type == gjson.Number {
		return value.Num
	}
	return 0
}
