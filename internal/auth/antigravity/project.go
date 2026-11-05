package antigravity

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antigravitywire"
	"github.com/tidwall/gjson"
)

// NeoPi's Cloud Code Assist project discovery (oauth/google-antigravity.ts).
const (
	loadCodeAssistPath = "/v1internal:loadCodeAssist"
	onboardUserPath    = "/v1internal:onboardUser"
	operationsPath     = "/v1internal/"

	freeTierID            = "free-tier"
	onboardTimeoutMs      = 30_000
	onboardPollIntervalMs = 1_000

	// loadCodeAssistMetadata is ANTIGRAVITY_LOAD_CODE_ASSIST_METADATA, the
	// metadata native Antigravity control-plane requests carry.
	loadCodeAssistMetadata = `{"metadata":{"ideType":"ANTIGRAVITY"}}`
)

// DiscoverProject is discoverProject: loadCodeAssist, the free-tier
// eligibility gate, onboarding when the account has no tier yet, and the
// refreshed project id.
func (f *Flow) DiscoverProject(ctx context.Context, accessToken string) (string, error) {
	f.report("Checking Cloud Code Assist account status...")
	projectID, err := f.discoverProject(ctx, accessToken)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		var oauthErr *OAuthError
		if errorsAs(err, &oauthErr) {
			return "", err
		}
		return "", oauthError("discovery", fmt.Sprintf("Could not discover an Antigravity project. %v", err))
	}
	return projectID, nil
}

func (f *Flow) discoverProject(ctx context.Context, accessToken string) (string, error) {
	initial, err := f.loadCodeAssist(ctx, accessToken)
	if err != nil {
		return "", err
	}
	if err := assertFreeTierEligible(initial); err != nil {
		return "", err
	}
	if !hasMessageField(initial, "currentTier") {
		f.report("Provisioning the Antigravity free tier...")
		if err := f.onboardUser(ctx, accessToken); err != nil {
			return "", err
		}
	}
	f.report("Refreshing Cloud Code Assist project...")
	refreshed, err := f.loadCodeAssist(ctx, accessToken)
	if err != nil {
		return "", err
	}
	if projectID := refreshed.project; projectID != "" {
		return projectID, nil
	}
	return "", oauthError("provisioning", "loadCodeAssist did not return a cloudaicompanionProject")
}

// loadCodeAssistResponse is the validated LoadCodeAssistResponse.
type loadCodeAssistResponse struct {
	payload    gjson.Result
	project    string
	tiers      []string
	ineligible []ineligibleTier
}

type ineligibleTier struct {
	tierID        string
	reasonMessage string
	validationURL string
}

// hasMessageField is hasMessageField: the field must be present and not null.
func hasMessageField(response loadCodeAssistResponse, field string) bool {
	value := response.payload.Get(field)
	return value.Exists() && value.Type != gjson.Null
}

func (f *Flow) loadCodeAssist(ctx context.Context, accessToken string) (loadCodeAssistResponse, error) {
	first, err := f.postLoadCodeAssist(ctx, accessToken, loadCodeAssistMetadata)
	if err != nil {
		return loadCodeAssistResponse{}, err
	}
	// A response without a paid tier re-asks with the discovered project.
	if !hasMessageField(first, "paidTier") && first.project != "" {
		body := fmt.Sprintf(`{"cloudaicompanionProject":%s,"metadata":{"ideType":"ANTIGRAVITY"}}`, jsonQuote(first.project))
		second, errSecond := f.postLoadCodeAssist(ctx, accessToken, body)
		if errSecond != nil {
			return loadCodeAssistResponse{}, errSecond
		}
		return second, nil
	}
	return first, nil
}

func (f *Flow) postLoadCodeAssist(ctx context.Context, accessToken, body string) (loadCodeAssistResponse, error) {
	payload, err := f.cloudCodeCall(ctx, accessToken, "loadCodeAssist", CloudCodeEndpoint+loadCodeAssistPath, http.MethodPost, body, neoPiTimeout)
	if err != nil {
		return loadCodeAssistResponse{}, err
	}
	return parseLoadCodeAssistResponse(payload)
}

// parseLoadCodeAssistResponse validates the payload as NeoPi's arktype schema
// does and projects the fields discovery reads.
func parseLoadCodeAssistResponse(payload gjson.Result) (loadCodeAssistResponse, error) {
	if problems := validateLoadCodeAssist(payload); len(problems) > 0 {
		return loadCodeAssistResponse{}, oauthError("provisioning",
			"failed to unmarshal LoadCodeAssistResponse: "+strings.Join(problems, "\n"))
	}
	response := loadCodeAssistResponse{payload: payload}
	if project, ok := scalarString(payload.Get("cloudaicompanionProject")); ok {
		response.project = project
	}
	payload.Get("allowedTiers").ForEach(func(_, tier gjson.Result) bool {
		if id, ok := scalarString(tier.Get("id")); ok {
			response.tiers = append(response.tiers, id)
		}
		return true
	})
	payload.Get("ineligibleTiers").ForEach(func(_, tier gjson.Result) bool {
		entry := ineligibleTier{}
		entry.tierID, _ = scalarString(tier.Get("tierId"))
		entry.reasonMessage, _ = scalarString(tier.Get("reasonMessage"))
		entry.validationURL, _ = scalarString(tier.Get("validationUrl"))
		response.ineligible = append(response.ineligible, entry)
		return true
	})
	return response, nil
}

func isFreeTierAllowed(response loadCodeAssistResponse) bool {
	for _, id := range response.tiers {
		if id == freeTierID {
			return true
		}
	}
	return false
}

func freeTierIneligibility(response loadCodeAssistResponse) (reasonMessage, validationURL string, found bool) {
	for _, tier := range response.ineligible {
		if tier.tierID != freeTierID {
			continue
		}
		if tier.reasonMessage == "" {
			return "", "", false
		}
		return tier.reasonMessage, tier.validationURL, true
	}
	return "", "", false
}

func assertFreeTierEligible(response loadCodeAssistResponse) error {
	if isFreeTierAllowed(response) {
		return nil
	}
	reasonMessage, validationURL, found := freeTierIneligibility(response)
	if !found {
		return nil
	}
	message := reasonMessage
	if validationURL != "" {
		message += "\n" + validationURL
	}
	return oauthError("provisioning", message)
}

// remainingOnboardTime is remainingOnboardTime: the time left before the
// 30-second onboarding deadline, or NeoPi's timeout error.
func remainingOnboardTime(deadline time.Time, now time.Time) (time.Duration, error) {
	remaining := deadline.Sub(now)
	if remaining > 0 {
		return remaining, nil
	}
	return 0, oauthError("timeout", fmt.Sprintf("onboardUser timed out after %dms", onboardTimeoutMs))
}

// onboardUser provisions the free tier and polls the long-running operation
// until it is done.
func (f *Flow) onboardUser(ctx context.Context, accessToken string) error {
	deadline := f.now().Add(onboardTimeoutMs * time.Millisecond)
	remaining, err := remainingOnboardTime(deadline, f.now())
	if err != nil {
		return err
	}
	payload, err := f.cloudCodeCall(ctx, accessToken, "onboardUser", CloudCodeEndpoint+onboardUserPath, http.MethodPost,
		`{"tierId":"free-tier","metadata":{"ideType":"ANTIGRAVITY"}}`, remaining)
	if err != nil {
		return err
	}
	operation, err := parseOnboardOperation(payload)
	if err != nil {
		return err
	}
	for {
		if operation.done {
			if operation.hasError {
				return oauthError("provisioning", "OnboardUser operation failed: "+describeOperationError(operation.error))
			}
			if !operation.hasResponse {
				return oauthError("provisioning", "failed to unmarshal OnboardUserResponse")
			}
			return nil
		}
		remaining, err = remainingOnboardTime(deadline, f.now())
		if err != nil {
			return err
		}
		pollInterval := onboardPollIntervalMs * time.Millisecond
		if remaining < pollInterval {
			pollInterval = remaining
		}
		if errSleep := f.sleep(ctx, pollInterval); errSleep != nil {
			return errSleep
		}
		if errCancel := ctx.Err(); errCancel != nil {
			return errCancel
		}
		if operation.name == "" {
			return oauthError("provisioning", "onboardUser returned an operation without a name")
		}
		remaining, err = remainingOnboardTime(deadline, f.now())
		if err != nil {
			return err
		}
		payload, err = f.cloudCodeCall(ctx, accessToken, "onboardUser operation", CloudCodeEndpoint+operationsPath+operation.name,
			http.MethodGet, "", remaining)
		if err != nil {
			return err
		}
		operation, err = parseOnboardOperation(payload)
		if err != nil {
			return err
		}
	}
}

type onboardOperation struct {
	payload     gjson.Result
	name        string
	done        bool
	hasError    bool
	error       gjson.Result
	hasResponse bool
}

func parseOnboardOperation(payload gjson.Result) (onboardOperation, error) {
	if problems := validateOnboardOperation(payload); len(problems) > 0 {
		return onboardOperation{}, oauthError("provisioning",
			"failed to unmarshal OnboardUser operation: "+strings.Join(problems, "\n"))
	}
	operation := onboardOperation{payload: payload}
	operation.name, _ = scalarString(payload.Get("name"))
	if done := payload.Get("done"); done.Type == gjson.True {
		operation.done = true
	}
	if operationError := payload.Get("error"); operationError.Exists() && operationError.Type != gjson.Null {
		operation.hasError = true
		operation.error = operationError
	}
	if response := payload.Get("response"); response.Exists() && response.Type != gjson.Null {
		operation.hasResponse = true
	}
	return operation, nil
}

// describeOperationError is describeOperationError: the code and message, or
// the error object as JSON.
func describeOperationError(operationError gjson.Result) string {
	message := operationError.Get("message")
	if message.Type == gjson.String && message.Str != "" {
		if code := operationError.Get("code"); code.Type == gjson.Number {
			return jsNumberString(code.Num) + ": " + message.Str
		}
		return message.Str
	}
	return jsStringify(operationError)
}

// cloudCodeCall is requestCloudCodeAssist: one Cloud Code Assist request with
// the native client headers, NeoPi's labels in errors and a JSON response.
func (f *Flow) cloudCodeCall(ctx context.Context, accessToken, label, rawURL, method, body string, timeout time.Duration) (gjson.Result, error) {
	if err := ctx.Err(); err != nil {
		return gjson.Result{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req *http.Request
	var err error
	if reader != nil {
		req, err = http.NewRequestWithContext(requestCtx, method, rawURL, reader)
	} else {
		req, err = http.NewRequestWithContext(requestCtx, method, rawURL, nil)
	}
	if err != nil {
		return gjson.Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	antigravitywire.SetClientHeaders(req.Header, "")
	resp, err := f.client().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return gjson.Result{}, ctxErr
		}
		return gjson.Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	text, errRead := readBody(resp)
	if errRead != nil {
		return gjson.Result{}, errRead
	}
	if resp.StatusCode != http.StatusOK {
		return gjson.Result{}, &OAuthError{
			Kind:    "provisioning",
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("%s failed: %d %s: %s", label, resp.StatusCode, statusText(resp), text),
		}
	}
	// Bun's response.json() yields null for an empty body and rejects any other
	// non-JSON text, whitespace included.
	if text == "" {
		return gjson.Parse("null"), nil
	}
	if !gjson.Valid(text) {
		return gjson.Result{}, fmt.Errorf("Failed to parse JSON")
	}
	return gjson.Parse(text), nil
}

func statusText(resp *http.Response) string {
	if text := http.StatusText(resp.StatusCode); text != "" {
		return text
	}
	if _, text, found := strings.Cut(resp.Status, " "); found {
		return text
	}
	return ""
}

func errorsAs(err error, target **OAuthError) bool {
	oauthErr, ok := err.(*OAuthError)
	if !ok {
		return false
	}
	*target = oauthErr
	return true
}

// extractGoogleValidationURL is extractGoogleValidationUrl: the validation URL
// of a VALIDATION_REQUIRED error detail, when the body carries one.
func extractGoogleValidationURL(errorBody string) string {
	if !strings.Contains(errorBody, "VALIDATION_REQUIRED") {
		return ""
	}
	start := strings.IndexByte(errorBody, '{')
	if start == -1 {
		return ""
	}
	parsed := parseJSON(errorBody[start:])
	for _, detail := range parsed.Get("error.details").Array() {
		if detail.Get("reason").String() != "VALIDATION_REQUIRED" {
			continue
		}
		if url := detail.Get("metadata.validation_url"); url.Type == gjson.String {
			return url.Str
		}
	}
	return ""
}

// formatGoogleValidationRequired is formatGoogleValidationRequiredMessage.
func formatGoogleValidationRequired(validationURL, nextAction, email string) string {
	account := ""
	if email != "" {
		account = " for " + email
	}
	return fmt.Sprintf("Account verification required%s. Visit %s to continue, then %s.", account, validationURL, nextAction)
}
