// Command fetch_codex_models uses NeoPi's Codex discovery protocol and saves its
// unmodified, account-scoped upstream catalog for inspection or offline use.
//
// Usage:
//
//	go run ./cmd/fetch_codex_models [flags]
//
// Flags:
//
//	--auths-dir       <path>  Directory containing auth JSON files (default: config auth-dir)
//	--config          <path>  Config file path                 (default: "config.toml"; legacy YAML imports once)
//	--output          <path>  Output JSON file path             (default: "codex_client_models.json")
//	--client-version <ver>   Codex client_version query value  (default: NeoPi's current version)
//	--pretty                 Pretty-print the output JSON      (default: true)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func init() {
	logging.SetupBaseLogger()
	log.SetLevel(log.InfoLevel)
}

func main() {
	var authsDir string
	var configPath string
	var outputPath string
	var clientVersion string
	var pretty bool

	flag.StringVar(&authsDir, "auths-dir", "", "Directory containing auth JSON files (overrides config auth-dir)")
	flag.StringVar(&configPath, "config", "", "Configure File Path")
	flag.StringVar(&outputPath, "output", "codex_client_models.json", "Output JSON file path")
	flag.StringVar(&clientVersion, "client-version", codexauth.ModelsClientVersion, "Codex client_version query value")
	flag.BoolVar(&pretty, "pretty", true, "Pretty-print the output JSON")
	flag.Parse()
	authsDirOverridden := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "auths-dir" {
			authsDirOverridden = true
		}
	})

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	if strings.TrimSpace(configPath) == "" {
		configPath = config.DefaultConfigFile(wd)
	}
	cfg, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to load config file %s: %v\n", configPath, err)
		os.Exit(1)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	if !authsDirOverridden {
		authsDir = cfg.AuthDir
	} else if strings.TrimSpace(authsDir) != "" && !strings.HasPrefix(strings.TrimSpace(authsDir), "~") && !filepath.IsAbs(authsDir) {
		authsDir = filepath.Join(wd, authsDir)
	}
	if authsDir, err = util.ResolveAuthDir(authsDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to resolve auth directory: %v\n", err)
		os.Exit(1)
	}
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(wd, outputPath)
	}

	fmt.Printf("Scanning auth files in: %s\n", authsDir)

	fileStore := sdkauth.NewFileTokenStore()
	fileStore.SetBaseDir(authsDir)

	ctx := context.Background()
	auths, err := fileStore.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to list auth files: %v\n", err)
		os.Exit(1)
	}
	if len(auths) == 0 {
		fmt.Fprintf(os.Stderr, "error: no auth files found in %s\n", authsDir)
		os.Exit(1)
	}

	chosen := findCodexAuth(auths)
	if chosen == nil {
		fmt.Fprintf(os.Stderr, "error: no enabled codex auth found in %s\n", authsDir)
		os.Exit(1)
	}

	fmt.Printf("Using auth: id=%s label=%s\n", chosen.ID, chosen.Label)

	accessToken, refreshed, err := ensureAccessToken(ctx, cfg, fileStore, chosen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to prepare codex access token: %v\n", err)
		os.Exit(1)
	}
	if refreshed {
		fmt.Println("Refreshed Codex access token.")
	}

	fmt.Println("Fetching Codex model list from upstream...")

	raw, count, err := fetchModels(ctx, cfg, chosen, accessToken, clientVersion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to fetch codex models: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Discovered %d usable models; saving the original upstream catalog.\n", count)

	if pretty {
		raw, err = prettyJSON(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to format JSON: %v\n", err)
			os.Exit(1)
		}
	}

	if err = os.WriteFile(outputPath, raw, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to write output file %s: %v\n", outputPath, err)
		os.Exit(1)
	}

	fmt.Printf("Model list saved to: %s\n", outputPath)
}

func findCodexAuth(auths []*coreauth.Auth) *coreauth.Auth {
	for _, auth := range auths {
		if auth == nil || auth.Disabled {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		if metaStringValue(auth.Metadata, "access_token") == "" && metaStringValue(auth.Metadata, "refresh_token") == "" {
			continue
		}
		return auth
	}
	return nil
}

func ensureAccessToken(ctx context.Context, cfg *config.Config, store *sdkauth.FileTokenStore, auth *coreauth.Auth) (string, bool, error) {
	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken != "" {
		if expiresAt, ok := auth.ExpirationTime(); ok && time.Now().Add(coreauth.CodexRefreshLead).Before(expiresAt) {
			return accessToken, false, nil
		}
	}
	if coreauth.IsRefreshLocked(auth) {
		return "", false, coreauth.ErrRefreshLocked
	}
	if metaStringValue(auth.Metadata, "refresh_token") == "" {
		if accessToken != "" {
			return accessToken, false, nil
		}
		return "", false, fmt.Errorf("missing access_token and refresh_token")
	}
	refreshed, errRefresh := executor.NewCodexExecutor(cfg).Refresh(ctx, auth)
	if errRefresh != nil {
		return "", false, errRefresh
	}
	if _, errSave := store.Save(ctx, refreshed); errSave != nil {
		return "", false, fmt.Errorf("failed to save refreshed auth: %w", errSave)
	}
	return metaStringValue(refreshed.Metadata, "access_token"), true, nil
}

func fetchModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, accessToken, clientVersion string) ([]byte, int, error) {
	base := strings.TrimSpace(auth.Attributes["base_url"])
	if base == "" {
		base = metaStringValue(auth.Metadata, "base_url")
	}
	catalog, errFetch := codexauth.FetchModels(ctx, helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0), codexauth.ModelDiscoveryOptions{
		AccessToken:   accessToken,
		AccountID:     metaStringValue(auth.Metadata, "account_id"),
		BaseURL:       base,
		ClientVersion: clientVersion,
	})
	if errFetch != nil {
		return nil, 0, errFetch
	}
	if catalog.RejectedStatus != 0 {
		return nil, 0, fmt.Errorf("Codex discovery rejected the credential: HTTP %d", catalog.RejectedStatus)
	}
	return catalog.Raw, len(catalog.Models), nil
}

func prettyJSON(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func metaStringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	default:
		return ""
	}
}
