package neopool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	zaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// accessHTTPClient validates keys. It never follows redirects (a key must not be
// replayed to another host) and never surfaces upstream bodies, which can echo
// partial keys or private account details.
var accessHTTPClient = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

const maxValidatedModels = 2000

// modelRoute is the upstream API (and its base URL) a model list advertises
// for one model, in OpenCode's vocabulary: chat-completions, responses,
// messages or generate-content.
type modelRoute struct {
	dialect string
	baseURL string
}

// advertisedDialects lists the model-list "api" keys NeoProxy can route, most
// compatible first, for models that advertise more than one.
var advertisedDialects = []string{"chat-completions", "responses", "messages", "generate-content"}

// listUpstreamModels proves a key works by listing the models it can use. Model
// lists that describe each model's API (OpenCode) also yield its route; models
// that advertise only APIs NeoProxy cannot speak are left out.
func listUpstreamModels(ctx context.Context, style, base, key string) ([]string, map[string]modelRoute, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, nil, accessErr(http.StatusBadRequest, "The base URL must be an http(s) address such as http://localhost:11434/v1, without credentials or a query string.")
	}
	if style == "anthropic" {
		models, err := (&anthropicnpi.Client{HTTP: accessHTTPClient}).Models(ctx, key, base)
		if err != nil {
			var upstream *anthropicnpi.ModelDiscoveryError
			if errors.As(err, &upstream) && (upstream.Status == http.StatusUnauthorized || upstream.Status == http.StatusForbidden) {
				return nil, nil, &AccessError{Status: http.StatusBadRequest, keyRejected: true, Message: fmt.Sprintf("The provider rejected this key (HTTP %d). Check that the whole key was pasted and that it is still active.", upstream.Status)}
			}
			return nil, nil, accessErr(http.StatusBadGateway, "%s", err)
		}
		if len(models) == 0 {
			return nil, nil, accessErr(http.StatusBadRequest, "The provider accepted the key but listed no models for it. Check the key's project or permissions.")
		}
		ids := make([]string, len(models))
		for i, model := range models {
			ids[i] = model.ID
		}
		return ids, nil, nil
	}
	var target string
	header := http.Header{}
	switch style {
	case "gemini":
		target = base + "/v1beta/models?pageSize=1000"
		header.Set("x-goog-api-key", key)
	default:
		target = base + "/models"
		if key != "" {
			header.Set("Authorization", "Bearer "+key)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, accessErr(http.StatusBadRequest, "The base URL is not a valid address.")
	}
	req.Header = header
	req.Header.Set("Accept", "application/json")
	// Some providers sit behind bot protection that rejects Go's default
	// User-Agent with a 403 page (Nous Portal does), which would read as a bad key.
	req.Header.Set("User-Agent", "NeoProxy")
	resp, err := accessHTTPClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout") {
			return nil, nil, accessErr(http.StatusBadGateway, "%s did not answer within 20 seconds. Check the address and that the server is running, then try again.", parsed.Host)
		}
		return nil, nil, accessErr(http.StatusBadGateway, "Could not reach %s. Check the address and that the server is running, then try again.", parsed.Host)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, nil, &AccessError{Status: http.StatusBadRequest, keyRejected: true, Message: fmt.Sprintf("The provider rejected this key (HTTP %d). Check that the whole key was pasted and that it is still active.", resp.StatusCode)}
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil, accessErr(http.StatusBadRequest, "%s has no model list at %s. Check the base URL; OpenAI-compatible servers usually end in /v1.", parsed.Host, parsed.Path+"/models")
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, nil, accessErr(http.StatusBadRequest, "%s redirected the model list request (HTTP %d). Use the final address as the base URL.", parsed.Host, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, nil, accessErr(http.StatusServiceUnavailable, "%s is rate-limiting requests right now (HTTP 429); the key itself was not rejected. Wait a minute and try again.", parsed.Host)
	case resp.StatusCode != http.StatusOK:
		return nil, nil, accessErr(http.StatusBadGateway, "The provider answered HTTP %d when listing models. Try again in a moment, or check the provider's status page.", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, nil, accessErr(http.StatusBadGateway, "The model list from %s could not be read. Try again in a moment.", parsed.Host)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
			// API is OpenCode's per-model transport map; other providers may
			// use the name for anything, so it is decoded leniently below.
			API json.RawMessage `json:"api"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"models"`
	}
	if err = json.Unmarshal(data, &body); err != nil {
		return nil, nil, accessErr(http.StatusBadGateway, "%s answered, but not with a model list. Check that the base URL points at the API, not a web page.", parsed.Host)
	}
	seen := map[string]bool{}
	models := []string{}
	add := func(id string) {
		id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
		if id != "" && !seen[id] && len(models) < maxValidatedModels {
			seen[id] = true
			models = append(models, id)
		}
	}
	routes := map[string]modelRoute{}
	for _, m := range body.Data {
		var apis map[string]struct {
			BaseURL string `json:"base_url"`
		}
		if len(m.API) == 0 || json.Unmarshal(m.API, &apis) != nil || len(apis) == 0 {
			add(m.ID)
			continue
		}
		for _, dialect := range advertisedDialects {
			if api, ok := apis[dialect]; ok {
				add(m.ID)
				routes[strings.TrimSpace(m.ID)] = modelRoute{dialect: dialect, baseURL: strings.TrimRight(strings.TrimSpace(api.BaseURL), "/")}
				break
			}
		}
	}
	for _, m := range body.Models {
		add(firstNonEmpty(m.ID, m.Name))
	}
	if len(models) == 0 {
		return nil, nil, accessErr(http.StatusBadRequest, "The provider accepted the key but listed no models for it. Check the key's project or permissions.")
	}
	sort.Strings(models)
	return models, routes, nil
}

// validateCatalogKey lists the models a key can use at base and returns the
// endpoint that accepted it. A provider with regional endpoints is tried region
// by region (preferred first, when it is one of them), because its keys are
// region-locked. Models the provider lists but chat clients cannot use are
// dropped so they never reach the model list. routes carries the API each
// model is served over (see modelRoutes).
func validateCatalogKey(ctx context.Context, info ProviderInfo, base, preferred, key string) (string, []string, map[string]modelRoute, error) {
	if registry.ResponsesProviderForBaseURL(base) != "" {
		return base, nil, nil, validateResponsesKey(ctx, base, key)
	}
	if info.ID == "zai" {
		return zaiauth.CodingPlanBaseURL, nil, nil, validateZaiKey(ctx, key)
	}
	// Nous Portal and Marineris are checked the way NeoPi's own extensions do.
	switch info.ID {
	case "nous-portal":
		return validateNousKey(ctx, base, key)
	case "marineris":
		return validateMarinerisKey(ctx, base, key)
	}
	candidates := []string{base}
	if regions := catalogRegions(info.ID); len(regions) > 0 {
		preferred = strings.TrimRight(strings.TrimSpace(preferred), "/")
		if preferred != "" && !slices.Contains(regions, preferred) {
			return "", nil, nil, accessErr(http.StatusBadRequest, "%s keys work only at its regional endpoints (%s); %s is not one of them.", info.Name, strings.Join(regions, ", "), preferred)
		}
		candidates = make([]string, 0, len(regions))
		if preferred != "" {
			candidates = append(candidates, preferred)
		}
		for _, region := range regions {
			if region != preferred {
				candidates = append(candidates, region)
			}
		}
	}
	var models []string
	var advertised map[string]modelRoute
	var err error
	for _, candidate := range candidates {
		if models, advertised, err = listUpstreamModels(ctx, info.APIKey.Style, candidate, key); err == nil {
			base = candidate
			break
		}
		var rejected *AccessError
		if !errors.As(err, &rejected) || !rejected.keyRejected {
			return "", nil, nil, err
		}
	}
	if err != nil {
		if len(candidates) > 1 {
			return "", nil, nil, accessErr(http.StatusBadRequest, "%s rejected this key in every region. Check that the whole key was pasted and that its subscription is active.", info.Name)
		}
		return "", nil, nil, err
	}
	if len(info.skipModels) > 0 {
		models = slices.DeleteFunc(models, func(id string) bool {
			return slices.ContainsFunc(info.skipModels, func(prefix string) bool { return strings.HasPrefix(id, prefix) })
		})
		if len(models) == 0 {
			return "", nil, nil, accessErr(http.StatusBadRequest, "%s accepted the key but lists no chat models for it.", info.Name)
		}
	}
	return base, models, modelRoutes(info, base, models, advertised), nil
}

// validateZaiKey is NeoPi's Z.ai key check (validate "chat-completions"): a
// one-token chat completion with glm-5.2 at the coding-plan endpoint. The
// account then serves NeoPi's Z.ai catalog, so no model list is kept.
func validateZaiKey(ctx context.Context, key string) error {
	body := `{"model":"glm-5.2","messages":[{"role":"user","content":"ping"}],"max_tokens":1,"temperature":0}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, zaiauth.CodingPlanBaseURL+"/chat/completions", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := accessHTTPClient.Do(req)
	if err != nil {
		return accessErr(http.StatusBadGateway, "Could not reach Z.ai to check the key. Try again in a moment.")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &AccessError{Status: http.StatusBadRequest, keyRejected: true, Message: fmt.Sprintf("Z.ai rejected this key (HTTP %d). Check that the whole key was pasted and that its GLM Coding Plan is active.", resp.StatusCode)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return accessErr(http.StatusServiceUnavailable, "Z.ai is rate-limiting requests right now (HTTP 429); the key itself was not rejected. Wait a minute and try again.")
	default:
		return accessErr(http.StatusBadGateway, "Z.ai answered HTTP %d when checking the key. Try again in a moment.", resp.StatusCode)
	}
}

// modelRoutes decides each model's upstream API: what the model list
// advertises, else the provider's family rules; models left on chat
// completions at the entry's base URL need no route. An advertised base URL is
// kept only on the entry's own scheme and host, so a model list can never send
// the key elsewhere.
func modelRoutes(info ProviderInfo, base string, models []string, advertised map[string]modelRoute) map[string]modelRoute {
	out := map[string]modelRoute{}
	for _, id := range models {
		route := advertised[id]
		if route.dialect == "" {
			for _, rule := range info.dialectRules {
				if strings.HasPrefix(id, rule.prefix) {
					route.dialect = rule.dialect
					break
				}
			}
		}
		if route.baseURL != "" && (route.baseURL == base || !sameOrigin(route.baseURL, base)) {
			route.baseURL = ""
		}
		if (route.dialect == "" || route.dialect == "chat-completions") && route.baseURL == "" {
			continue
		}
		out[id] = route
	}
	return out
}

func sameOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && ua.User == nil && strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

func accountModelRoutes(routes map[string]modelRoute) map[string]coreauth.AccountModelRoute {
	if len(routes) == 0 {
		return nil
	}
	out := make(map[string]coreauth.AccountModelRoute, len(routes))
	for id, route := range routes {
		out[id] = coreauth.AccountModelRoute{Dialect: route.dialect, BaseURL: route.baseURL}
	}
	return out
}
