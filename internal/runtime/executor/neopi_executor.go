package executor

import (
	"context"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// NeoPiExecutor reuses the existing three-wire translation, usage reporting and
// final payload-config barrier. Only the selected broker row is sent upstream;
// bearer resolution and live account/model checks happen again on every retry.
type NeoPiExecutor struct {
	*OpenAICompatExecutor
	prepare func(context.Context, *coreauth.Auth, string) (*coreauth.Auth, error)
}

func NewNeoPiExecutor(provider string, cfg *config.Config, prepare func(context.Context, *coreauth.Auth, string) (*coreauth.Auth, error)) *NeoPiExecutor {
	return &NeoPiExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor(provider, cfg), prepare: prepare}
}

func (e *NeoPiExecutor) selected(ctx context.Context, a *coreauth.Auth, model string, opts coreexecutor.Options) (*coreauth.Auth, error) {
	if a == nil || opts.Metadata[coreexecutor.PinnedAuthMetadataKey] != a.ID {
		return nil, statusErr{code: http.StatusForbidden, msg: "NeoPi source requires an account-scoped virtual model"}
	}
	if e.prepare == nil {
		return nil, statusErr{code: http.StatusServiceUnavailable, msg: "NeoPi source unavailable"}
	}
	out, err := e.prepare(ctx, a, thinking.ParseSuffix(model).ModelName)
	if err != nil || out == nil {
		return nil, statusErr{code: http.StatusForbidden, msg: "NeoPi source account or model is no longer available", credentialScoped: true}
	}
	return out, nil
}

func (e *NeoPiExecutor) Execute(ctx context.Context, a *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	selected, err := e.selected(ctx, a, req.Model, opts)
	if err != nil {
		return coreexecutor.Response{}, err
	}
	return e.OpenAICompatExecutor.Execute(ctx, selected, req, opts)
}

func (e *NeoPiExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	selected, err := e.selected(ctx, a, req.Model, opts)
	if err != nil {
		return nil, err
	}
	return e.OpenAICompatExecutor.ExecuteStream(ctx, selected, req, opts)
}

func (e *NeoPiExecutor) CountTokens(ctx context.Context, a *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	selected, err := e.selected(ctx, a, req.Model, opts)
	if err != nil {
		return coreexecutor.Response{}, err
	}
	return e.OpenAICompatExecutor.CountTokens(ctx, selected, req, opts)
}

// Arbitrary management HTTP calls cannot disclose a source bearer or dispatch an
// unpinned request. Normal inference uses Execute/ExecuteStream.
func (e *NeoPiExecutor) PrepareRequest(*http.Request, *coreauth.Auth) error {
	return fmt.Errorf("NeoPi source requires scoped inference")
}

func (e *NeoPiExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("NeoPi source requires scoped inference")
}

func (e *NeoPiExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}

// The pin is transport framing, not a configurable payload field or dynamically
// resolved custom header. Apply it last so client headers cannot change the row.
func applyNeoPiCredentialPin(req *http.Request, attrs map[string]string) {
	if attrs["neopi_source_id"] != "" {
		req.Header.Set("X-Neopi-Credential-Id", attrs["neopi_credential_id"])
	}
}

// Read-only validation is allowed after the payload barrier. Reject an attempted
// model rewrite rather than overwriting user configuration or escaping a grant.
func validateNeoPiPayload(a *coreauth.Auth, payload []byte, model string) error {
	if a != nil && a.Attributes["neopi_source_id"] != "" && gjson.GetBytes(payload, "model").String() != model {
		return statusErr{code: http.StatusForbidden, msg: "Configured payload model is outside the selected NeoPi route"}
	}
	return nil
}

func neoPiHTTPClient(client *http.Client, a *coreauth.Auth) *http.Client {
	if a == nil || a.Attributes["neopi_source_id"] == "" {
		return client
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copyClient
}
