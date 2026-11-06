package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func newConfigV8TOMLRouter(t *testing.T, name, raw string) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		router.Handle(method, "/v8/management/config", h.ConfigV8)
		router.Handle(method, "/v8/management/config.toml", h.ConfigV8)
	}
	return router, path
}

func serveConfigV8(router *gin.Engine, method, url, body, ifMatch string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	router.ServeHTTP(recorder, req)
	return recorder
}

func decodeErrorBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", recorder.Body.String(), err)
	}
	return body
}

func errorShape(body map[string]any) []string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestConfigV8TOMLInvalidBodyReportsFormatSpecificErrors(t *testing.T) {
	raw := "config-version = 8\nserver = {port = 8317}\naccess = {api-keys = [\"k\"]}\n"
	router, path := newConfigV8TOMLRouter(t, "config.toml", raw)

	jsonSyntax := serveConfigV8(router, http.MethodPut, "/v8/management/config", `{"server":`, "")
	tomlSyntax := serveConfigV8(router, http.MethodPut, "/v8/management/config.toml", "[server\nport = 1", "")
	if jsonSyntax.Code != http.StatusBadRequest || tomlSyntax.Code != http.StatusBadRequest {
		t.Fatalf("syntax errors: json=%d toml=%d, want 400", jsonSyntax.Code, tomlSyntax.Code)
	}
	jsonBody, tomlBody := decodeErrorBody(t, jsonSyntax), decodeErrorBody(t, tomlSyntax)
	if jsonBody["error"] != "invalid_json" || tomlBody["error"] != "invalid_body" {
		t.Fatalf("syntax error codes: json=%v toml=%v", jsonBody, tomlBody)
	}
	if !reflect.DeepEqual(errorShape(jsonBody), []string{"error"}) || !reflect.DeepEqual(errorShape(tomlBody), []string{"error", "message"}) {
		t.Fatalf("unexpected syntax error shapes: json=%v toml=%v", jsonBody, tomlBody)
	}
	if !strings.Contains(tomlBody["message"].(string), "line") {
		t.Fatalf("TOML syntax error lacks a position: %v", tomlBody["message"])
	}

	// Parsable documents that fail to decode as a configuration.
	jsonInvalid := serveConfigV8(router, http.MethodPut, "/v8/management/config", `{"config-version":8,"server":{"port":"not-a-number"}}`, "")
	tomlInvalid := serveConfigV8(router, http.MethodPut, "/v8/management/config.toml", "config-version = 8\n[server]\nport = \"not-a-number\"\n", "")
	if jsonInvalid.Code != tomlInvalid.Code {
		t.Fatalf("invalid config status: json=%d toml=%d body json=%s toml=%s", jsonInvalid.Code, tomlInvalid.Code, jsonInvalid.Body.String(), tomlInvalid.Body.String())
	}
	jsonBody, tomlBody = decodeErrorBody(t, jsonInvalid), decodeErrorBody(t, tomlInvalid)
	if jsonBody["error"] != "invalid_config" || !reflect.DeepEqual(errorShape(jsonBody), errorShape(tomlBody)) || jsonBody["error"] != tomlBody["error"] {
		t.Fatalf("invalid config bodies differ: json=%v toml=%v", jsonBody, tomlBody)
	}

	if data, _ := os.ReadFile(path); string(data) != raw {
		t.Fatalf("rejected writes changed the file:\n%s", data)
	}
}

func TestConfigV8TOMLWritesKeepLiveFileFormat(t *testing.T) {
	t.Run("toml body into native file", func(t *testing.T) {
		router, path := newConfigV8TOMLRouter(t, "config.toml", "config-version = 8\nserver = {port = 8317}\naccess = {api-keys = [\"k\"]}\n")
		get := serveConfigV8(router, http.MethodGet, "/v8/management/config.toml", "", "")
		if get.Code != http.StatusOK || get.Header().Get("X-Config-Format") != "toml" || !strings.HasPrefix(get.Header().Get("Content-Type"), "application/toml") {
			t.Fatalf("GET config.toml: %d %v %s", get.Code, get.Header(), get.Body.String())
		}
		if !strings.Contains(strings.ReplaceAll(get.Body.String(), `"`, ""), "[server]") || !strings.Contains(get.Body.String(), "port = 8317") {
			t.Fatalf("GET config.toml body:\n%s", get.Body.String())
		}
		body := strings.Replace(get.Body.String(), "port = 8317", "port = 8320", 1)
		put := serveConfigV8(router, http.MethodPut, "/v8/management/config.toml", body, get.Header().Get("ETag"))
		if put.Code != http.StatusOK || put.Header().Get("X-Config-Format") != "toml" {
			t.Fatalf("PUT config.toml: %d %v %s", put.Code, put.Header(), put.Body.String())
		}
		data, _ := os.ReadFile(path)
		if config.DetectConfigFormat("", data) != config.ConfigFormatTOML || !strings.Contains(string(data), "port = 8320") {
			t.Fatalf("toml file after TOML PUT:\n%s", data)
		}
		if put.Header().Get("ETag") != `"`+config.ComputeSourceHash(data)+`"` {
			t.Fatalf("PUT ETag %q does not match the file", put.Header().Get("ETag"))
		}
		stale := serveConfigV8(router, http.MethodPut, "/v8/management/config.toml", body, get.Header().Get("ETag"))
		if stale.Code != http.StatusPreconditionFailed {
			t.Fatalf("stale If-Match: %d", stale.Code)
		}
	})

	t.Run("toml and JSON bodies into commented file", func(t *testing.T) {
		raw := "# Live TOML file.\nconfig-version = 8\n\n[server]\n# Listen port.\nport = 8317\n\n[access]\napi-keys = [\"k\"]\n"
		router, path := newConfigV8TOMLRouter(t, "config.toml", raw)
		get := serveConfigV8(router, http.MethodGet, "/v8/management/config.toml", "", "")
		if get.Code != http.StatusOK || get.Header().Get("X-Config-Format") != "toml" {
			t.Fatalf("GET config.toml: %d %v %s", get.Code, get.Header(), get.Body.String())
		}
		body := strings.Replace(get.Body.String(), "port = 8317", "port = 8321", 1)
		put := serveConfigV8(router, http.MethodPut, "/v8/management/config.toml", body, get.Header().Get("ETag"))
		if put.Code != http.StatusOK || put.Header().Get("X-Config-Format") != "toml" {
			t.Fatalf("PUT config.toml: %d %v %s", put.Code, put.Header(), put.Body.String())
		}
		data, _ := os.ReadFile(path)
		text := string(data)
		if !strings.Contains(strings.ReplaceAll(text, `"`, ""), "[server]") || !strings.Contains(text, "port = 8321") || !strings.Contains(text, "# Listen port.") {
			t.Fatalf("toml file after TOML PUT lost format or comments:\n%s", text)
		}
		loaded, err := config.LoadConfig(path)
		if err != nil || loaded.Port != 8321 {
			t.Fatalf("reload: port=%v err=%v", loaded, err)
		}
		patch := serveConfigV8(router, http.MethodPut, "/v8/management/config", `{"config-version":8,"server":{"port":8322},"access":{"api-keys":["k"]}}`, "")
		if patch.Code != http.StatusOK || patch.Header().Get("X-Config-Format") != "toml" {
			t.Fatalf("JSON PUT: %d %s", patch.Code, patch.Body.String())
		}
		if data, _ = os.ReadFile(path); !strings.Contains(string(data), "port = 8322") {
			t.Fatalf("toml file after JSON PUT:\n%s", data)
		}
	})
}
