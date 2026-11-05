package googlewire

import (
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// isUsageLimitStatus is NeoPi's isUsageLimitStatus: a status whose body must
// keep the RPC residue so a billing cap is not replayed as a throttle.
func isUsageLimitStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusPaymentRequired
}

// ExtractErrorMessage is extractGoogleErrorMessage: the human message of a
// non-2xx Google body, keeping the RPC status/details residue on a usage-limit
// status.
func ExtractErrorMessage(errorText string, status int) string {
	if errorText == "" {
		return "Unknown error"
	}
	parsed := gjson.Parse(errorText)
	message := parsed.Get("error.message").String()
	if message == "" {
		return errorText
	}
	if !isUsageLimitStatus(status) {
		return message
	}
	statusName := parsed.Get("error.status").String()
	details := parsed.Get("error.details")
	if statusName == "" && !details.Exists() {
		return message
	}
	fields := []jsjson.Field{{Name: "status", Value: jsjson.Marshal(statusName)}}
	if details.Exists() {
		fields = append(fields, jsjson.Field{Name: "details", Value: jsjson.Normalize([]byte(details.Raw))})
	}
	return message + " " + string(jsjson.Object(jsjson.Field{Name: "error", Value: jsjson.Object(fields...)}))
}

// StreamError is the error an in-band `error` frame of a 200 generateContent
// stream raises (googleStreamChunkError), keeping the RPC status so a
// RESOURCE_EXHAUSTED quota failure is still classified as one.
type StreamError struct {
	Code    int
	Message string
}

func (e *StreamError) Error() string   { return e.Message }
func (e *StreamError) StatusCode() int { return e.Code }

// StreamChunkError is googleStreamChunkError over one stream frame.
func StreamChunkError(payload []byte) error {
	errorBody := gjson.GetBytes(payload, "error")
	message, status := errorBody.Get("message").String(), errorBody.Get("status").String()
	detail := message
	switch {
	case message != "" && status != "":
		detail = message + " (" + status + ")"
	case message == "" && status == "":
		detail = "unknown error"
	}
	residue := ""
	if details := errorBody.Get("details"); details.IsArray() && len(details.Array()) > 0 {
		residue = " " + string(jsjson.Object(jsjson.Field{Name: "error", Value: jsjson.Object(
			jsjson.Field{Name: "status", Value: jsjson.Marshal(status)},
			jsjson.Field{Name: "details", Value: jsjson.Normalize([]byte(details.Raw))})}))
	}
	streamError := &StreamError{Code: http.StatusBadGateway, Message: fmt.Sprintf("Google API stream error: %s%s", detail, residue)}
	if code := errorBody.Get("code"); code.Type == gjson.Number && code.Int() >= 400 {
		streamError.Code = int(code.Int())
	}
	return streamError
}

// MapUsage is mapGoogleUsage: Gemini usageMetadata as prompt/completion counts,
// with cached tokens split out of the prompt.
func MapUsage(metadata gjson.Result) (input, output, cacheRead, reasoning, total int) {
	candidates := int(metadata.Get("candidatesTokenCount").Int())
	thinking := int(metadata.Get("thoughtsTokenCount").Int())
	total = int(metadata.Get("totalTokenCount").Int())
	prompt := int(metadata.Get("promptTokenCount").Int())
	if prompt == 0 {
		prompt = max(0, total-candidates-thinking)
	}
	cacheRead = min(int(metadata.Get("cachedContentTokenCount").Int()), prompt)
	return prompt - cacheRead, candidates + thinking, cacheRead, thinking, total
}
