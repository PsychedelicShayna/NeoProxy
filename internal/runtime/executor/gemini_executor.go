// Package executor provides runtime execution capabilities for various AI service providers.
// It includes stateless executors that handle API requests, streaming responses,
// token counting, and authentication refresh for different AI service providers.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// glEndpoint is the base URL for the Google Generative Language API.
	glEndpoint = "https://generativelanguage.googleapis.com"

	// glAPIVersion is the API version used for Gemini requests.
	glAPIVersion = "v1beta"

	// streamScannerBuffer is the buffer size for SSE stream scanning.
	streamScannerBuffer = helps.StreamScannerBuffer
)

// GeminiExecutor is a stateless executor for the official Gemini API using API keys.
// It supports regular and streaming requests to the Google Generative Language API.
type GeminiExecutor struct {
	// cfg holds the application configuration.
	cfg        *config.Config
	identifier string
	// neopi serves every non-interactions request: NeoPi's google provider.
	neopi *NeoPiGoogleExecutor
}

// NewGeminiExecutor creates a new Gemini executor instance.
//
// Parameters:
//   - cfg: The application configuration
//
// Returns:
//   - *GeminiExecutor: A new Gemini executor instance
func NewGeminiExecutor(cfg *config.Config) *NeoPiGoogleExecutor {
	return NewNeoPiGoogleExecutor("gemini", cfg, NeoPiGoogleHooks{})
}

// NewGeminiInteractionsExecutor creates a Gemini executor bound to the native Interactions provider.
func NewGeminiInteractionsExecutor(cfg *config.Config) *GeminiExecutor {
	return &GeminiExecutor{cfg: cfg, identifier: "gemini-interactions",
		neopi: NewNeoPiGoogleExecutor("gemini-interactions", cfg, NeoPiGoogleHooks{})}
}

// Identifier returns the executor identifier.
func (e *GeminiExecutor) Identifier() string {
	if e == nil || strings.TrimSpace(e.identifier) == "" {
		return "gemini"
	}
	return e.identifier
}

// RequestToFormat reports the upstream request format used after auth selection.
func (e *GeminiExecutor) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if strings.EqualFold(strings.TrimSpace(e.Identifier()), "gemini-interactions") && nativeInteractionsSourceFormat(opts.SourceFormat) {
		return sdktranslator.FormatInteractions
	}
	return sdktranslator.FormatGemini
}

// PrepareRequest injects Gemini credentials into the outgoing HTTP request.
func (e *GeminiExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey := geminiAPIKey(auth)
	if apiKey != "" {
		req.Header.Set("x-goog-api-key", apiKey)
		req.Header.Del("Authorization")
	} else {
		req.Header.Del("x-goog-api-key")
		req.Header.Del("Authorization")
	}
	applyGeminiHeaders(req, auth)
	return nil
}

// HttpRequest injects Gemini credentials into the request and executes it.
func (e *GeminiExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("gemini executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming request to the Gemini API.
// It translates the request to Gemini format, sends it to the API, and translates
// the response back to the requested format.
//
// Parameters:
//   - ctx: The context for the request
//   - auth: The authentication information
//   - req: The request to execute
//   - opts: Additional execution options
//
// Returns:
//   - cliproxyexecutor.Response: The response from the API
//   - error: An error if the request fails
func (e *GeminiExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if shouldExecuteNativeInteractions(auth, opts) {
		ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
		if opts.Alt == "responses/compact" {
			return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
		}
		return e.executeInteractions(ctx, auth, req, opts)
	}
	return e.neopi.Execute(ctx, auth, req, opts)
}

// ExecuteStream performs a streaming request to the Gemini API.
func (e *GeminiExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if shouldExecuteNativeInteractions(auth, opts) {
		ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
		if opts.Alt == "responses/compact" {
			return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
		}
		return e.executeInteractionsStream(ctx, auth, req, opts)
	}
	return e.neopi.ExecuteStream(ctx, auth, req, opts)
}

func geminiStreamHasFinishSignal(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	for _, path := range []string{
		"candidates.#.finishReason",
		"response.candidates.#.finishReason",
		"promptFeedback.blockReason",
		"response.promptFeedback.blockReason",
	} {
		res := gjson.GetBytes(payload, path)
		if res.IsArray() {
			for _, item := range res.Array() {
				if strings.TrimSpace(item.String()) != "" {
					return true
				}
			}
		} else if res.Exists() && strings.TrimSpace(res.String()) != "" {
			return true
		}
	}
	return false
}

func (e *GeminiExecutor) executeInteractions(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	targetName := thinking.ParseSuffix(req.Model).ModelName
	apiKey := geminiAPIKey(auth)
	reporter := helps.NewExecutorUsageReporter(ctx, e, targetName, auth)
	defer reporter.TrackFailure(ctx, &err)

	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated, body, err := translateGeminiInteractionsRequestPair(ctx, e.cfg, targetName, req.Payload, opts, false, isCompat)
	if err != nil {
		return resp, err
	}
	if gjson.GetBytes(body, "model").Exists() && targetName != "" {
		body = helps.SetStringIfDifferent(body, "model", targetName)
	}
	body, err = applyGeminiInteractionsThinking(body, req, opts)
	if err != nil {
		return resp, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	fromProtocol := opts.SourceFormat.String()
	body = sanitizeGeminiInteractionsUnsupportedInputIDs(body)
	continuation, body := helps.PrepareAntigravityInteractions(auth, req, opts, targetName, body)

	baseURL := resolveGeminiBaseURL(auth)
	url := fmt.Sprintf("%s/%s/interactions", baseURL, glAPIVersion)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, targetName, "interactions", fromProtocol, "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errRequest != nil {
		return resp, errRequest
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyGeminiHeaders(httpReq, auth, opts.Headers)
	applyGeminiInteractionsRequestHeaders(httpReq, opts.Headers)
	applyGeminiInteractionsRevisionHeader(httpReq)

	authID, authLabel, authType, authValue := geminiAuthLogFields(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini executor: close interactions response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return resp, err
	}
	continuation.Observe(data)
	reporter.ObserveResponseModel(data)
	targetFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatInteractions, targetFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), body, data, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	reporter.Publish(ctx, helps.ParseInteractionsUsage(data))
	if targetFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *GeminiExecutor) executeInteractionsStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	targetName := thinking.ParseSuffix(req.Model).ModelName
	apiKey := geminiAPIKey(auth)
	reporter := helps.NewExecutorUsageReporter(ctx, e, targetName, auth)
	defer reporter.TrackFailure(ctx, &err)

	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated, body, err := translateGeminiInteractionsRequestPair(ctx, e.cfg, targetName, req.Payload, opts, true, isCompat)
	if err != nil {
		return nil, err
	}
	if gjson.GetBytes(body, "model").Exists() && targetName != "" {
		body = helps.SetStringIfDifferent(body, "model", targetName)
	}
	body, err = applyGeminiInteractionsThinking(body, req, opts)
	if err != nil {
		return nil, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	fromProtocol := opts.SourceFormat.String()
	body = sanitizeGeminiInteractionsUnsupportedInputIDs(body)
	continuation, body := helps.PrepareAntigravityInteractions(auth, req, opts, targetName, body)
	body = helps.SetBoolIfDifferent(body, "stream", true)
	baseURL := resolveGeminiBaseURL(auth)
	url := fmt.Sprintf("%s/%s/interactions", baseURL, glAPIVersion)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, targetName, "interactions", fromProtocol, "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errRequest != nil {
		return nil, errRequest
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyGeminiHeaders(httpReq, auth, opts.Headers)
	applyGeminiInteractionsRequestHeaders(httpReq, opts.Headers)
	applyGeminiInteractionsRevisionHeader(httpReq)

	authID, authLabel, authType, authValue := geminiAuthLogFields(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, _ := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini executor: close interactions error response body error: %v", errClose)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		return nil, statusErr{code: httpResp.StatusCode, msg: string(data)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	go func() {
		defer close(out)
		defer reporter.EnsurePublished(ctx)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("gemini executor: close interactions stream body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, streamScannerBuffer)
		originalRequest := opts.OriginalRequest
		if len(originalRequest) == 0 {
			originalRequest = req.Payload
		}
		claudeInputTokens := helps.NewClaudeInputTokenState(opts.SourceFormat, sdktranslator.FormatInteractions, responseFormat, originalRequest)
		var streamUsage helps.StreamUsageBuffer
		defer streamUsage.Publish(ctx, reporter)
		var param any
		helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatInteractions, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), body, &param)
		var frame []byte
		emitFrame := func() bool {
			rawFrame := bytes.Clone(frame)
			trimmed := bytes.TrimSpace(rawFrame)
			frame = frame[:0]
			if len(trimmed) == 0 {
				return true
			}
			payload := geminiInteractionsSSEPayload(rawFrame)
			if len(payload) == 0 && geminiInteractionsSSEDone(rawFrame) {
				payload = []byte("[DONE]")
			}
			if len(payload) == 0 && len(trimmed) > 0 && trimmed[0] == '{' {
				payload = trimmed
			}
			if len(payload) > 0 {
				continuation.Observe(payload)
				reporter.ObserveResponseModel(payload)
				if detail, ok := helps.ParseInteractionsStreamUsage(payload); ok {
					streamUsage.Observe(detail, true)
				}
			}
			if responseFormat == sdktranslator.FormatInteractions {
				visibleFrame := append(bytes.TrimRight(rawFrame, "\r\n"), '\n', '\n')
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: visibleFrame}:
				case <-ctx.Done():
					return false
				}
				return true
			}
			if len(payload) == 0 {
				return true
			}
			var lines [][]byte
			lines = helps.TranslateStreamWithClaudeInputTokens(ctx, sdktranslator.FormatInteractions, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), body, payload, &param, claudeInputTokens)
			helps.RecordApplyPatchStreamFailure(ctx, param, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			for i := range lines {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: lines[i]}:
				case <-ctx.Done():
					return false
				}
			}
			if helps.StopApplyPatchStream(ctx, param, reporter, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
				return false
			}
			return true
		}
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				if !emitFrame() {
					return
				}
				continue
			}
			if len(frame) > 0 {
				frame = append(frame, '\n')
			}
			frame = append(frame, line...)
		}
		if !emitFrame() {
			return
		}
		if helps.EndApplyPatchStream(ctx, param, reporter, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
			return
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// CountTokens estimates locally; NeoPi never asks Google to count.
func (e *GeminiExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.neopi.CountTokens(ctx, auth, req, opts)
}

// Refresh refreshes the authentication credentials (no-op for Gemini API key).
func (e *GeminiExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

func geminiAPIKey(a *cliproxyauth.Auth) string {
	if a == nil {
		return ""
	}
	if a.Attributes != nil {
		if v := a.Attributes["api_key"]; v != "" {
			return v
		}
	}
	return ""
}

func resolveGeminiBaseURL(auth *cliproxyauth.Auth) string {
	base := glEndpoint
	if auth != nil && auth.Attributes != nil {
		if custom := strings.TrimSpace(auth.Attributes["base_url"]); custom != "" {
			base = strings.TrimRight(custom, "/")
		}
	}
	if base == "" {
		return glEndpoint
	}
	return base
}

func (e *GeminiExecutor) resolveGeminiConfig(auth *cliproxyauth.Auth) *config.GeminiKey {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range e.cfg.GeminiKey {
		entry := &e.cfg.GeminiKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range e.cfg.GeminiKey {
			entry := &e.cfg.GeminiKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func shouldExecuteNativeInteractions(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) bool {
	return nativeInteractionsSourceFormat(opts.SourceFormat) && isNativeInteractionsAuth(auth)
}

func nativeInteractionsSourceFormat(format sdktranslator.Format) bool {
	switch format {
	case sdktranslator.FormatInteractions, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, sdktranslator.FormatGemini:
		return true
	default:
		return false
	}
}

// sanitizeGeminiInteractionsUnsupportedInputIDs aligns input step IDs with the
// official Gemini Interactions API schema:
// - `function_call` (FunctionCallStep) requires `id` and rejects `call_id`
// - `function_result` (FunctionResultStep) requires `call_id` and rejects `id`
// - other steps and content parts do not support `id`
func sanitizeGeminiInteractionsUnsupportedInputIDs(body []byte) []byte {
	return helps.SanitizeGeminiInteractionsUnsupportedInputIDs(body)
}

func translateGeminiInteractionsRequestBody(ctx context.Context, cfg *config.Config, model string, payload []byte, opts cliproxyexecutor.Options, stream, isCompat bool) ([]byte, error) {
	return helps.TranslateGeminiInteractionsRequestBody(ctx, cfg, model, payload, opts, stream, isCompat)
}

// translateGeminiInteractionsRequestPair translates the working payload and the
// payload-config baseline. Identical inputs are translated once, including plugin
// hooks. The baseline is captured before model and thinking mutations, and the
// working buffer is a separate copy so those mutations cannot change it. Distinct
// inputs keep the existing order: working payload first, then the payload-config
// source.
func translateGeminiInteractionsRequestPair(ctx context.Context, cfg *config.Config, model string, payload []byte, opts cliproxyexecutor.Options, stream, isCompat bool) (original, working []byte, err error) {
	return helps.TranslateGeminiInteractionsRequestPair(ctx, cfg, model, payload, opts, stream, isCompat)
}

func geminiInteractionsPayloadConfigSource(ctx context.Context, cfg *config.Config, model string, payload []byte, opts cliproxyexecutor.Options, stream, isCompat bool) ([]byte, error) {
	return helps.TranslateGeminiInteractionsRequestBody(ctx, cfg, model, geminiInteractionsPayloadConfigInput(opts, payload), opts, stream, isCompat)
}

func geminiInteractionsPayloadConfigInput(opts cliproxyexecutor.Options, payload []byte) []byte {
	if len(opts.OriginalRequest) == 0 {
		return payload
	}
	return opts.OriginalRequest
}

// geminiInteractionsSameByteSlice reports whether both slices describe the same
// bytes of the same backing array. It compares identity rather than content so
// the check stays constant time on large payloads.
func geminiInteractionsSameByteSlice(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}

func isNativeInteractionsAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "gemini-interactions")
}

func applyGeminiInteractionsThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, error) {
	return helps.ApplyGeminiInteractionsThinking(body, req, opts)
}

func applyGeminiInteractionsRevisionHeader(req *http.Request) {
	helps.ApplyGeminiInteractionsRevisionHeader(req)
}

func applyGeminiInteractionsRequestHeaders(req *http.Request, headers http.Header) {
	helps.ApplyGeminiInteractionsRequestHeaders(req, headers)
}

func geminiInteractionsSSEPayload(frame []byte) []byte {
	return helps.GeminiInteractionsSSEPayload(frame)
}

func geminiInteractionsSSEDone(frame []byte) bool {
	return helps.GeminiInteractionsSSEDone(frame)
}

func geminiAuthLogFields(auth *cliproxyauth.Auth) (string, string, string, string) {
	if auth == nil {
		return "", "", "", ""
	}
	authType, authValue := auth.AccountInfo()
	return auth.ID, auth.Label, authType, authValue
}

func applyGeminiHeaders(req *http.Request, auth *cliproxyauth.Auth, clientHeaders ...http.Header) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs, clientHeaders...)
}

func fixGeminiImageAspectRatio(modelName string, rawJSON []byte) []byte {
	if modelName == "gemini-2.5-flash-image-preview" {
		aspectRatioResult := gjson.GetBytes(rawJSON, "generationConfig.imageConfig.aspectRatio")
		if aspectRatioResult.Exists() {
			contents := gjson.GetBytes(rawJSON, "contents")
			contentArray := contents.Array()
			if len(contentArray) > 0 {
				hasInlineData := false
			loopContent:
				for i := 0; i < len(contentArray); i++ {
					parts := contentArray[i].Get("parts").Array()
					for j := 0; j < len(parts); j++ {
						if parts[j].Get("inlineData").Exists() {
							hasInlineData = true
							break loopContent
						}
					}
				}

				if !hasInlineData {
					emptyImageBase64ed, _ := util.CreateWhiteImageBase64(aspectRatioResult.String())
					emptyImagePart := []byte(`{"inlineData":{"mime_type":"image/png","data":""}}`)
					emptyImagePart, _ = sjson.SetBytes(emptyImagePart, "inlineData.data", emptyImageBase64ed)
					newPartsJson := []byte(`[]`)
					newPartsJson, _ = sjson.SetRawBytes(newPartsJson, "-1", []byte(`{"text": "Based on the following requirements, create an image within the uploaded picture. The new content *MUST* completely cover the entire area of the original picture, maintaining its exact proportions, and *NO* blank areas should appear."}`))
					newPartsJson, _ = sjson.SetRawBytes(newPartsJson, "-1", emptyImagePart)

					parts := contentArray[0].Get("parts").Array()
					for j := 0; j < len(parts); j++ {
						newPartsJson, _ = sjson.SetRawBytes(newPartsJson, "-1", []byte(parts[j].Raw))
					}

					rawJSON, _ = sjson.SetRawBytes(rawJSON, "contents.0.parts", newPartsJson)
					rawJSON, _ = sjson.SetRawBytes(rawJSON, "generationConfig.responseModalities", []byte(`["IMAGE", "TEXT"]`))
				}
			}
			rawJSON, _ = sjson.DeleteBytes(rawJSON, "generationConfig.imageConfig")
		}
	}
	return rawJSON
}

// SupportsApplyPatch reports the actual executor contract, independent of its provider name.
func (e *GeminiExecutor) SupportsApplyPatch() bool { return e != nil }
