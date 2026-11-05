package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/oauthpage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// AntigravityAuthenticator runs NeoPi's google-antigravity sign-in: an
// authorization-code grant with PKCE and a hex state through the loopback
// callback, followed by userinfo and Cloud Code Assist project discovery.
type AntigravityAuthenticator struct {
	// CallbackPort is the preferred loopback port; another free port serves
	// when it is taken, unless the login requested a port explicitly.
	CallbackPort int
}

// NewAntigravityAuthenticator constructs a new authenticator instance.
func NewAntigravityAuthenticator() *AntigravityAuthenticator {
	return &AntigravityAuthenticator{CallbackPort: antigravity.CallbackPort}
}

// Provider returns the provider key for antigravity.
func (AntigravityAuthenticator) Provider() string { return "antigravity" }

// RefreshLead refreshes five minutes before the stored expiry, which NeoPi
// already stores five minutes early; the manager needs a positive lead to
// schedule the refresh at all.
func (AntigravityAuthenticator) RefreshLead() *time.Duration {
	lead := 5 * time.Minute
	return &lead
}

// Login executes the browser callback login, or the paste-only login when no
// browser is used.
func (a *AntigravityAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	pkce, errPKCE := antigravity.GeneratePKCE()
	if errPKCE != nil {
		return nil, errPKCE
	}
	state, errState := antigravity.GenerateState()
	if errState != nil {
		return nil, errState
	}
	client := util.SetProxy(&cfg.SDKConfig, &http.Client{})
	port := a.CallbackPort
	if port <= 0 {
		port = antigravity.CallbackPort
	}
	if opts.CallbackPort > 0 {
		port = opts.CallbackPort
	}

	if opts.NoBrowser {
		if opts.Prompt == nil {
			return nil, fmt.Errorf("antigravity authentication in no-browser mode requires an interactive prompt")
		}
		redirectURI := antigravity.RedirectURI(port)
		fmt.Printf("Visit the following URL to continue Antigravity authentication:\n%s\n\n",
			antigravity.BuildAuthURL(state, pkce.Challenge, redirectURI))
		// NeoPi keeps asking until the pasted text carries a usable code.
		for {
			manualInputCh, manualInputErrCh := misc.AsyncPrompt(opts.Prompt, "Paste the Antigravity callback URL or authorization code: ")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case errInput := <-manualInputErrCh:
				return nil, fmt.Errorf("failed to read antigravity input: %w", errInput)
			case input := <-manualInputCh:
				if code, ok := antigravityManualCode(input, state); ok {
					return completeAntigravityLogin(ctx, client, code, redirectURI, pkce.Verifier)
				}
			}
		}
	}

	callback, actualPort, errStart := startAntigravityCallbackServer(port, opts.CallbackPort > 0, state)
	if errStart != nil {
		return nil, fmt.Errorf("failed to start antigravity oauth callback server: %w", errStart)
	}
	defer callback.stop()

	redirectURI := antigravity.RedirectURI(actualPort)
	authURL := antigravity.BuildAuthURL(state, pkce.Challenge, redirectURI)

	fmt.Println("Opening browser for Antigravity authentication...")
	if !browser.IsAvailable() {
		log.Warn("No browser available; please open the URL manually")
		util.PrintSSHTunnelInstructions(actualPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
		log.Warnf("Failed to open browser automatically: %v", errOpen)
		util.PrintSSHTunnelInstructions(actualPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Antigravity authentication callback...")

	callbackCh := make(chan antigravityCallbackResult, 1)
	go func() {
		result, errWait := callback.wait(ctx, 5*time.Minute)
		if errWait != nil {
			select {
			case callbackCh <- antigravityCallbackResult{Err: errWait}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case callbackCh <- result:
		case <-ctx.Done():
		}
	}()

	var manualPromptC <-chan time.Time
	if opts.Prompt != nil {
		manualPromptTimer := time.NewTimer(5 * time.Second)
		manualPromptC = manualPromptTimer.C
		defer manualPromptTimer.Stop()
	}
	var manualInputCh <-chan string
	var manualInputErrCh <-chan error
	finish := func(result antigravityCallbackResult) (*coreauth.Auth, error) {
		if result.Err != nil {
			return nil, fmt.Errorf("antigravity oauth callback failed: %w", result.Err)
		}
		if result.Error != "" {
			return nil, fmt.Errorf("antigravity oauth error: %s", result.Error)
		}
		return completeAntigravityLogin(ctx, client, result.Code, redirectURI, pkce.Verifier)
	}
	const manualPromptMessage = "Paste the Antigravity callback URL or authorization code (or press Enter to keep waiting): "

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case result := <-callbackCh:
			return finish(result)

		case <-manualPromptC:
			manualPromptC = nil
			select {
			case result := <-callbackCh:
				return finish(result)
			default:
			}
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(opts.Prompt, manualPromptMessage)

		case input := <-manualInputCh:
			manualInputCh, manualInputErrCh = nil, nil
			if code, ok := antigravityManualCode(input, state); ok {
				return completeAntigravityLogin(ctx, client, code, redirectURI, pkce.Verifier)
			}
			// NeoPi ignores pasted text without a usable code and asks again.
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(opts.Prompt, manualPromptMessage)

		case errInput := <-manualInputErrCh:
			manualInputCh, manualInputErrCh = nil, nil
			if errInput != nil {
				log.Debugf("manual input prompt error: %v", errInput)
			}
		}
	}
}

// completeAntigravityLogin exchanges the code, discovers the project and
// records the credential, with a best-effort usage report for its identity.
func completeAntigravityLogin(ctx context.Context, client *http.Client, code, redirectURI, verifier string) (*coreauth.Auth, error) {
	credential, errExchange := antigravity.Exchange(ctx, client, code, redirectURI, verifier)
	if errExchange != nil {
		return nil, fmt.Errorf("antigravity: %w", errExchange)
	}
	if strings.TrimSpace(credential.Access) == "" {
		return nil, fmt.Errorf("antigravity: token exchange returned empty access token")
	}
	if strings.TrimSpace(credential.ProjectID) == "" {
		return nil, fmt.Errorf("antigravity: project discovery returned empty project")
	}
	if credential.Email != "" {
		log.Infof("antigravity: obtained project ID %s", util.HideAPIKey(credential.ProjectID))
	}
	now := time.Now()
	record := antigravity.BuildAuth(credential, now)
	usage, errUsage := antigravity.FetchUsage(ctx, antigravity.UsageRequest{
		Client:      client,
		AccessToken: credential.Access,
		ProjectID:   credential.ProjectID,
		Email:       credential.Email,
		ExpiresAt:   credential.ExpiresAt,
	})
	if errUsage != nil {
		log.Warnf("antigravity: usage report unavailable: %v", errUsage)
	}
	antigravity.ApplyUsage(record, usage, now)
	return record, nil
}

// antigravityManualCode is NeoPi's manual-input race: pasted text yields a
// code when parseCallbackInput finds one and any state it carries matches.
// Anything else is ignored and the login keeps waiting.
func antigravityManualCode(input, expectedState string) (string, bool) {
	parsed := antigravity.ParseCallbackInput(input)
	if parsed.Code == nil || *parsed.Code == "" {
		return "", false
	}
	if expectedState != "" && parsed.State != nil && *parsed.State != "" && *parsed.State != expectedState {
		return "", false
	}
	return *parsed.Code, true
}

// antigravityCallbackResult is one accepted browser callback: a code, or the
// denial the genuine redirect carried.
type antigravityCallbackResult struct {
	Code  string
	Error string
	Err   error
}

// antigravityCallbackServer is NeoPi's loopback callback: the rule's path
// answers the redirect, anything else is 404. A callback whose state is not
// ours is answered with the failure page but never settles the login.
type antigravityCallbackServer struct {
	server        *http.Server
	listener      net.Listener
	results       chan antigravityCallbackResult
	expectedState string
}

// startAntigravityCallbackServer binds the preferred port, falling back to a
// free one when the caller did not pin a port (NeoPi's allowPortFallback).
func startAntigravityCallbackServer(port int, pinned bool, expectedState string) (*antigravityCallbackServer, int, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", antigravity.CallbackHost, port))
	if err != nil {
		if pinned {
			return nil, 0, err
		}
		listener, err = net.Listen("tcp", fmt.Sprintf("%s:0", antigravity.CallbackHost))
		if err != nil {
			return nil, 0, err
		}
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	callback := &antigravityCallbackServer{
		listener:      listener,
		results:       make(chan antigravityCallbackResult, 1),
		expectedState: expectedState,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", callback.handle)
	callback.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if errServe := callback.server.Serve(listener); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.Debugf("antigravity oauth callback server error: %v", errServe)
		}
	}()
	return callback, actualPort, nil
}

func (c *antigravityCallbackServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != antigravity.CallbackPath {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	query := r.URL.Query()
	code := query.Get("code")
	state := query.Get("state")
	failure := query.Get("error")
	description := query.Get("error_description")
	if description == "" {
		description = failure
	}
	stateMatches := c.expectedState == "" || state == c.expectedState
	var detail string
	switch {
	case failure != "":
		detail = "Authorization failed: " + description
	case code == "":
		detail = "Missing authorization code"
	case !stateMatches:
		detail = "State mismatch - possible CSRF attack"
	}
	if detail == "" {
		c.settle(antigravityCallbackResult{Code: code})
		oauthpage.Write(w, http.StatusOK, oauthpage.Page{Provider: "antigravity", Success: true})
		return
	}
	// The redirect carries our state nonce only when it came from the genuine
	// authorization flow (the user denied consent); a forged error must not
	// abort the login.
	if failure != "" && stateMatches {
		c.settle(antigravityCallbackResult{Error: detail})
	}
	oauthpage.Write(w, http.StatusInternalServerError, oauthpage.Page{Provider: "antigravity", Detail: detail})
}

// settle records the first accepted callback.
func (c *antigravityCallbackServer) settle(result antigravityCallbackResult) {
	select {
	case c.results <- result:
	default:
	}
}

func (c *antigravityCallbackServer) wait(ctx context.Context, timeout time.Duration) (antigravityCallbackResult, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-c.results:
		return result, nil
	case <-ctx.Done():
		return antigravityCallbackResult{}, ctx.Err()
	case <-timer.C:
		return antigravityCallbackResult{}, errors.New("antigravity authentication timed out")
	}
}

func (c *antigravityCallbackServer) stop() {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c.server != nil {
		_ = c.server.Shutdown(shutdownCtx)
	}
}

// AntigravityDefaultCallbackPort is the loopback port NeoPi's rule prefers.
const AntigravityDefaultCallbackPort = antigravity.CallbackPort

// AntigravityDefaultCallbackURI returns the loopback redirect URI for the
// default port.
func AntigravityDefaultCallbackURI() string {
	return antigravity.RedirectURI(AntigravityDefaultCallbackPort)
}

// BuildAntigravityAuthURL generates the OAuth authorization URL for Antigravity.
func BuildAntigravityAuthURL(state, codeChallenge, redirectURI string) string {
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = AntigravityDefaultCallbackURI()
	}
	return antigravity.BuildAuthURL(state, codeChallenge, redirectURI)
}

// FetchAntigravityProjectID resolves an account's Cloud Code Assist project.
func FetchAntigravityProjectID(ctx context.Context, accessToken string, httpClient *http.Client) (string, error) {
	return antigravity.DiscoverProject(ctx, httpClient, accessToken)
}

// CompleteAntigravityOAuth performs the code exchange, the userinfo email and
// GCP project discovery using an injected HTTP client, returning an assembled
// *coreauth.Auth without touching the filesystem.
func CompleteAntigravityOAuth(ctx context.Context, code, redirectURI, codeVerifier string, httpClient *http.Client) (*coreauth.Auth, error) {
	return completeAntigravityLogin(ctx, httpClient, code, redirectURI, codeVerifier)
}
