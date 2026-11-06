package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// The management save path must write a real TOML document, not YAML or JSON
// with a .toml suffix. Exercise it entirely in memory apart from the config file.
func TestManagementSaveRoundTripWritesNativeTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	initial := "config-version = 8\n[server]\nport = 8317\n[plugins.configs.sample]\nenabled = false\nmode = 'keep-custom-value'\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PATCH("/v8/management/config", h.ConfigV8)
	router.GET("/v8/management/config.toml", h.ConfigV8)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(`{"server":{"port":9123},"plugins":{"configs":{"sample":{"enabled":true}}}}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("management save status = %d, want 200", response.Code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := config.ParseConfigDocument(data)
	if err != nil {
		t.Fatalf("management save is not native TOML: %v", err)
	}
	if got := configV8Node(document, []string{"plugins", "configs", "sample", "mode"}); got != "keep-custom-value" {
		t.Fatalf("opaque plugin value = %v, want retained value", got)
	}
	reloaded, err := config.DecodeConfig(data, config.ConfigFormatTOML)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Port != 9123 || !pluginInstanceEnabled(reloaded.Plugins.Configs["sample"]) {
		t.Fatal("management mutation did not round-trip through native TOML")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v8/management/config.toml", nil))
	if response.Code != http.StatusOK || response.Header().Get("X-Config-Format") != string(config.ConfigFormatTOML) {
		t.Fatalf("management TOML read status = %d, format = %q", response.Code, response.Header().Get("X-Config-Format"))
	}
	if _, err = config.ParseConfigDocument(response.Body.Bytes()); err != nil {
		t.Fatalf("management document read is not TOML: %v", err)
	}
}
