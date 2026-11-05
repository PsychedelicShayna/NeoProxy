package meta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialFileName(t *testing.T) {
	if got := CredentialFileName("user@example.com", ""); !strings.HasPrefix(got, "meta-user_example.com-") || !strings.HasSuffix(got, ".json") {
		t.Errorf("unexpected fileName: %s", got)
	}
	if got := CredentialFileName("", "12345"); got == "" || !filepath.IsLocal(got) {
		t.Errorf("unexpected subject-based fileName: %s", got)
	}
	if got := CredentialFileName("", ""); got != "meta-oauth.json" {
		t.Errorf("unexpected fallback fileName: %s", got)
	}
}

func TestSaveTokenToFile_PreservesCustomMetadata(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "meta-test.json")

	storage := &MetaTokenStorage{
		Type:        "meta",
		AuthKind:    "oauth",
		AccessToken: "test-access-token",
		APIKey:      "test-api-key",
		DCAToken:    "dca:token",
		Email:       "user@example.com",
	}
	storage.SetMetadata(map[string]any{
		"priority":        float64(10),
		"disable_cooling": true,
		"proxy_url":       "http://proxy:8080",
	})

	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}

	savedRaw, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("os.ReadFile error = %v", errRead)
	}

	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error = %v", errUnmarshal)
	}

	if saved["api_key"] != "test-api-key" {
		t.Errorf("api_key = %v, want test-api-key", saved["api_key"])
	}
	if saved["priority"] != float64(10) {
		t.Errorf("priority = %v, want 10", saved["priority"])
	}
	if saved["disable_cooling"] != true {
		t.Errorf("disable_cooling = %v, want true", saved["disable_cooling"])
	}
	if saved["proxy_url"] != "http://proxy:8080" {
		t.Errorf("proxy_url = %v, want http://proxy:8080", saved["proxy_url"])
	}
}

func TestSaveTokenToFile_DoesNotRestoreClearedCredentialFields(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "meta-test.json")

	// Pre-populate an auth file that had expired DCA credentials and custom configuration
	initial := map[string]any{
		"type":            "meta",
		"auth_kind":       "oauth",
		"access_token":    "old-dca-token",
		"dca_token":       "old-dca-token",
		"expired":         "2026-01-01T00:00:00Z",
		"dca_expired":     "2026-01-01T00:00:00Z",
		"dca_expires_at":  float64(1767225600),
		"priority":        float64(42),
		"models":          []any{"muse-latest"},
		"disable-cooling": true,
	}
	raw, err := json.Marshal(initial)
	if err != nil {
		t.Fatalf("json.Marshal error = %v", err)
	}
	if err := os.WriteFile(authFilePath, raw, 0600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	// Token storage after minting an API key: Expired is explicitly empty, new APIKey set
	storage := &MetaTokenStorage{
		Type:        "meta",
		AuthKind:    "oauth",
		AccessToken: "new-minted-key",
		APIKey:      "new-minted-key",
		DCAToken:    "new-dca-token",
		Expired:     "", // Deliberately cleared on refresh
		Email:       "user@example.com",
	}

	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile error = %v", errSave)
	}

	savedRaw, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("os.ReadFile error = %v", errRead)
	}

	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error = %v", errUnmarshal)
	}

	// Verify cleared credential fields were NOT restored from disk
	if val, exists := saved["expired"]; exists && val != "" {
		t.Errorf("SaveTokenToFile restored expired timestamp: %v", val)
	}
	if val, exists := saved["dca_expired"]; exists && val != "" {
		t.Errorf("SaveTokenToFile restored dca_expired timestamp: %v", val)
	}
	if _, exists := saved["dca_expires_at"]; exists {
		t.Errorf("SaveTokenToFile restored dca_expires_at timestamp")
	}

	// Verify new credentials were written
	if saved["api_key"] != "new-minted-key" {
		t.Errorf("api_key = %v, want new-minted-key", saved["api_key"])
	}
	if saved["access_token"] != "new-minted-key" {
		t.Errorf("access_token = %v, want new-minted-key", saved["access_token"])
	}

	// Verify non-credential configuration from disk was preserved
	if saved["priority"] != float64(42) {
		t.Errorf("priority = %v, want 42", saved["priority"])
	}
	if saved["disable-cooling"] != true {
		t.Errorf("disable-cooling = %v, want true", saved["disable-cooling"])
	}
	if models, ok := saved["models"].([]any); !ok || len(models) != 1 || models[0] != "muse-latest" {
		t.Errorf("models = %v, want [muse-latest]", saved["models"])
	}
}

func TestCredentialFileNameSeparatesAccounts(t *testing.T) {
	first := CredentialFileName("alice+work@example.com", "dca:first")
	second := CredentialFileName("alice_work@example.com", "dca:second")
	if first == second {
		t.Fatal("distinct accounts overwrite the same file")
	}
	if first != CredentialFileName("alice+work@example.com", "dca:replacement") {
		t.Fatal("re-login changed account file")
	}
	long := CredentialFileName(strings.Repeat("a", 300)+"@example.com", "")
	if len(long) > 255 || !filepath.IsLocal(long) {
		t.Fatal("filename cannot be saved on common filesystems")
	}
}

func TestSaveTokenToFileUsesCurrentSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.json")
	if err := os.WriteFile(path, []byte(`{"disabled":false,"priority":1,"notes":"removed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	storage := &MetaTokenStorage{AccessToken: "LLM|key", APIKey: "LLM|key", Metadata: map[string]any{"disabled": true, "priority": float64(2)}}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["disabled"] != true || saved["priority"] != float64(2) {
		t.Fatalf("restored old settings: %s", raw)
	}
	if _, exists := saved["notes"]; exists {
		t.Fatal("restored a deleted setting")
	}
}

func TestSaveTokenToFilePreservesFileOnEncodingFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.json")
	initial := `{"access_token":"LLM|previous"}`
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	storage := &MetaTokenStorage{AccessToken: "LLM|new", Metadata: map[string]any{"invalid": make(chan int)}}
	if err := storage.SaveTokenToFile(path); err == nil {
		t.Fatal("expected encoding failure")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != initial {
		t.Fatalf("failed save corrupted existing credential: %q", raw)
	}
}
