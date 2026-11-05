package neopool

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/marineris"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/nous"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// validateNousKey checks a Nous Portal inference key the way NeoPi's
// extension uses it: its own model discovery request. NeoPi never validates a
// pasted key at sign-in, so a key the portal rejects there is rejected here.
func validateNousKey(ctx context.Context, base, key string) (string, []string, map[string]modelRoute, error) {
	client := nous.NewClientWithHTTP(accessHTTPClient)
	rows, err := client.FetchModelRows(ctx, key)
	if err != nil {
		return "", nil, nil, upstreamKeyError(err, nousStatus(err))
	}
	ids := modelIDs(registry.NousModels(rows, nous.Provider))
	if len(ids) == 0 {
		return "", nil, nil, accessErr(http.StatusBadRequest, "The provider accepted the key but listed no models for it. Check the key's project or permissions.")
	}
	return base, ids, nil, nil
}

// validateMarinerisKey is the key check of NeoPi's Marineris login: the
// gateway address is normalized and the key must list at least one model.
func validateMarinerisKey(ctx context.Context, base, key string) (string, []string, map[string]modelRoute, error) {
	address, models, err := marineris.Validate(ctx, accessHTTPClient, base, key)
	if err != nil {
		var upstream *marineris.HTTPError
		status := 0
		if errors.As(err, &upstream) {
			status = upstream.Status
		}
		return "", nil, nil, upstreamKeyError(err, status)
	}
	return address, modelIDs(models), nil, nil
}

func nousStatus(err error) int {
	var upstream *nous.HTTPError
	if errors.As(err, &upstream) {
		return upstream.Status
	}
	return 0
}

// upstreamKeyError reports NeoPi's own message for a failed check; a 401 or
// 403 marks the key as rejected so regional fallbacks behave as elsewhere.
func upstreamKeyError(err error, status int) *AccessError {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &AccessError{Status: http.StatusBadRequest, keyRejected: true, Message: err.Error()}
	case http.StatusTooManyRequests:
		return &AccessError{Status: http.StatusServiceUnavailable, Message: err.Error()}
	}
	return &AccessError{Status: http.StatusBadRequest, Message: err.Error()}
}

func modelIDs(models []*registry.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	sort.Strings(ids)
	return ids
}
