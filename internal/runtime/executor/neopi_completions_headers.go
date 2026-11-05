package executor

import (
	"context"
	"net/http"
	"strconv"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// neoPiCompletionsDefaultIdleTimeoutMs is DEFAULT_STREAM_IDLE_TIMEOUT_MS, the
// floor NeoPi's first-event deadline starts from.
const neoPiCompletionsDefaultIdleTimeoutMs = 300_000

// neoPiCompletionsHeaders is the OpenAI client's request headers as NeoPi sends
// them for a Chat Completions row: the account's configured headers first,
// then the ones NeoPi's client enforces.
func neoPiCompletionsHeaders(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, key string, row *registry.NeoPiModel) http.Header {
	headers := make(http.Header)
	if auth != nil {
		request := (&http.Request{Header: headers}).WithContext(ctx)
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes, incoming)
	}
	headers.Set("Accept", "text/event-stream")
	headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	headers.Set("Authorization", "Bearer "+key)
	headers.Set("Connection", "keep-alive")
	headers.Set("Content-Type", "application/json")
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", openaiwire.NeoPiUserAgent)
	}
	headers.Set("X-Stainless-Timeout", neoPiCompletionsTimeoutHeader(row))
	return headers
}

// neoPiCompletionsTimeoutHeader is the pre-response deadline NeoPi announces:
// its first-event timeout, which never undershoots the stream idle timeout.
func neoPiCompletionsTimeoutHeader(row *registry.NeoPiModel) string {
	timeout := neoPiCompletionsDefaultIdleTimeoutMs
	if row != nil && row.CompletionsCompat != nil && row.CompletionsCompat.StreamIdleTimeoutMs > timeout {
		timeout = row.CompletionsCompat.StreamIdleTimeoutMs
	}
	return strconv.Itoa(timeout / 1000)
}
