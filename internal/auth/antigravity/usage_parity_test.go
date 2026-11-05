package antigravity

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestUsageMatchesNeoPi replays testdata/neopi_antigravity_usage_capture.json:
// NeoPi's own antigravityUsageProvider.fetchUsage against scripted Cloud Code
// Assist responses. Each fixture must issue the same requests and produce the
// same report, and NeoPi's counter ranking must scope the same limits.
func TestUsageMatchesNeoPi(t *testing.T) {
	var capture struct {
		Now      int64 `json:"now"`
		Fixtures []struct {
			Name   string `json:"name"`
			Params struct {
				Provider   string `json:"provider"`
				BaseURL    string `json:"baseUrl"`
				Credential struct {
					AccessToken string   `json:"accessToken"`
					ProjectID   string   `json:"projectId"`
					Email       string   `json:"email"`
					AccountID   string   `json:"accountId"`
					ExpiresAt   *float64 `json:"expiresAt"`
				} `json:"credential"`
			} `json:"params"`
			Responses []struct {
				Status int    `json:"status"`
				Body   string `json:"body"`
			} `json:"responses"`
			Requests []struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"requests"`
			Report  json.RawMessage `json:"report"`
			Ranking *struct {
				Models []struct {
					ModelID    string   `json:"modelId"`
					CounterKey *string  `json:"counterKey"`
					Scoped     []string `json:"scoped"`
					Primary    *string  `json:"primary"`
					BlockScope string   `json:"blockScope"`
				} `json:"models"`
				Healable []struct {
					BlockScope string   `json:"blockScope"`
					Limits     []string `json:"limits"`
				} `json:"healable"`
			} `json:"ranking"`
			Error *string `json:"error"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(readCapture(t, "neopi_antigravity_usage_capture.json"), &capture); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			responses := make([]scriptedResponse, len(fixture.Responses))
			for index, response := range fixture.Responses {
				responses[index] = scriptedResponse{Status: response.Status, Body: response.Body}
			}
			client := newCaptureClient(t, responses)
			request := UsageRequest{
				Client:      client.HTTPClient(),
				BaseURL:     fixture.Params.BaseURL,
				AccessToken: fixture.Params.Credential.AccessToken,
				ProjectID:   fixture.Params.Credential.ProjectID,
				Email:       fixture.Params.Credential.Email,
				AccountID:   fixture.Params.Credential.AccountID,
				Now:         func() time.Time { return time.UnixMilli(capture.Now) },
			}
			if fixture.Params.Credential.ExpiresAt != nil {
				request.ExpiresAt = time.UnixMilli(int64(*fixture.Params.Credential.ExpiresAt))
			}
			usage, err := FetchUsage(context.Background(), request)
			compareRequests(t, client.Requests(), fixture.Requests)
			if fixture.Error != nil {
				if err == nil || err.Error() != *fixture.Error {
					t.Fatalf("error = %v, want %q", err, *fixture.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(fixture.Report) == 0 || string(fixture.Report) == "null" {
				if usage != nil {
					t.Fatalf("usage = %+v, want none", usage)
				}
				return
			}
			if usage == nil {
				t.Fatal("usage = nil, want a report")
			}
			encoded, errMarshal := json.Marshal(usage)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			var got, want any
			if err = json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(fixture.Report, &want); err != nil {
				t.Fatal(err)
			}
			equalJSON(t, got, want)

			if fixture.Ranking == nil {
				return
			}
			for _, model := range fixture.Ranking.Models {
				if key := CounterKeyForModel(model.ModelID); (model.CounterKey == nil && key != "") || (model.CounterKey != nil && key != *model.CounterKey) {
					t.Errorf("counter key for %q = %q, want %v", model.ModelID, key, model.CounterKey)
				}
				var scoped []string
				for _, limit := range ScopeLimitsForModel(usage, model.ModelID) {
					scoped = append(scoped, limit.ID)
				}
				if !equalStrings(scoped, model.Scoped) {
					t.Errorf("scoped limits for %q = %v, want %v", model.ModelID, scoped, model.Scoped)
				}
				primary := ""
				if limit := RankingWindowLimit(usage, model.ModelID); limit != nil {
					primary = limit.ID
				}
				if (model.Primary == nil && primary != "") || (model.Primary != nil && primary != *model.Primary) {
					t.Errorf("primary limit for %q = %q, want %v", model.ModelID, primary, model.Primary)
				}
				if blockScope := BlockScope(model.ModelID); blockScope != model.BlockScope {
					t.Errorf("block scope for %q = %q, want %q", model.ModelID, blockScope, model.BlockScope)
				}
			}
			healable := HealableBlockScopes(usage)
			if len(healable) != len(fixture.Ranking.Healable) {
				t.Fatalf("healable scopes = %d, want %d", len(healable), len(fixture.Ranking.Healable))
			}
			for index, scope := range healable {
				expected := fixture.Ranking.Healable[index]
				var limits []string
				for _, limit := range scope.Limits {
					limits = append(limits, limit.ID)
				}
				if scope.BlockScope != expected.BlockScope || !equalStrings(limits, expected.Limits) {
					t.Errorf("healable %d = %s %v, want %s %v", index, scope.BlockScope, limits, expected.BlockScope, expected.Limits)
				}
			}
		})
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
