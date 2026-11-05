// Package antigravitywire is the Go port of NeoPi's Antigravity (Cloud Code
// Assist) wire identity: pi-catalog wire/gemini-headers.ts (client version,
// user agent, request headers), shared by sign-in, discovery, usage and the
// streaming executor so every request carries the one identity NeoPi sends.
package antigravitywire

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultVersion is DEFAULT_ANTIGRAVITY_VERSION, the offline fallback.
	DefaultVersion = "2.26.0"
	// DefaultChangelist is the captured `cl` value; the backend does not validate it.
	DefaultChangelist = "1005016104"

	// DailyEndpoint is ANTIGRAVITY_DAILY_ENDPOINT, the primary host.
	DailyEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	// SandboxEndpoint is ANTIGRAVITY_SANDBOX_ENDPOINT, the failover host.
	SandboxEndpoint = "https://daily-cloudcode-pa.sandbox.googleapis.com"

	manifestURL          = "https://antigravity-hub-auto-updater-974169037036.us-central1.run.app/manifest/latest-arm64-mac.yml"
	manifestFetchTimeout = 5 * time.Second
	// manifestRetry is ANTIGRAVITY_VERSION_RETRY_MS: failed lookups are not
	// retried before this delay, so request paths never stall per call.
	manifestRetry = 10 * time.Minute
)

var manifestVersionLine = regexp.MustCompile(`^\s*version\s*:\s*(?:"([^"]*)"|'([^']*)'|([^\s#]+))\s*(?:#.*)?$`)

var semver = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

var versionState struct {
	mu         sync.Mutex
	discovered string
	failedAt   time.Time
	// inflight is closed when the running lookup settles; nil when idle.
	inflight chan struct{}
}

// ParseManifestVersion is parseAntigravityManifestVersion: the `version:`
// line of an electron-builder update manifest, or "" when none is well formed.
func ParseManifestVersion(yamlText string) string {
	for _, line := range strings.Split(strings.ReplaceAll(yamlText, "\r\n", "\n"), "\n") {
		match := manifestVersionLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		version := strings.TrimSpace(match[1] + match[2] + match[3])
		if semver.MatchString(version) {
			return version
		}
		return ""
	}
	return ""
}

// Version is getAntigravityVersion: env override, then the manifest-discovered
// version, then the pinned fallback.
func Version() string {
	if v := os.Getenv("PI_AI_ANTIGRAVITY_VERSION"); v != "" {
		return v
	}
	versionState.mu.Lock()
	defer versionState.mu.Unlock()
	if versionState.discovered != "" {
		return versionState.discovered
	}
	return DefaultVersion
}

// EnsureVersion is ensureAntigravityVersion: it resolves the latest release
// from the official update manifest. Success is cached for the process
// lifetime; failures are silent and suppress further lookups for ten
// minutes. A lookup is shared by concurrent callers and bounded only by its
// own timeout; ctx ends this caller's wait without cancelling it.
func EnsureVersion(ctx context.Context, client *http.Client) {
	if os.Getenv("PI_AI_ANTIGRAVITY_VERSION") != "" {
		return
	}
	versionState.mu.Lock()
	if versionState.discovered != "" {
		versionState.mu.Unlock()
		return
	}
	if versionState.inflight == nil {
		if !versionState.failedAt.IsZero() && time.Since(versionState.failedAt) < manifestRetry {
			versionState.mu.Unlock()
			return
		}
		done := make(chan struct{})
		versionState.inflight = done
		go func() {
			version := fetchManifestVersion(client)
			versionState.mu.Lock()
			if version != "" {
				versionState.discovered = version
			} else {
				versionState.failedAt = time.Now()
			}
			versionState.inflight = nil
			versionState.mu.Unlock()
			close(done)
		}()
	}
	done := versionState.inflight
	versionState.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func fetchManifestVersion(client *http.Client) string {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(context.Background(), manifestFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return ""
	}
	request.Header.Set("Cache-Control", "no-cache")
	request.Header.Set("User-Agent", "electron-builder")
	response, err := client.Do(request)
	if err != nil {
		return ""
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return ""
	}
	return ParseManifestVersion(string(body))
}

// Platform is getAntigravityPlatform: the native Antigravity build the host
// would run. OSes without a native build report the Linux build.
func Platform() (osName, arch string) {
	switch runtime.GOOS {
	case "darwin":
		return "darwin", "arm64"
	case "windows":
		return "windows", "amd64"
	}
	return "linux", "amd64"
}

// UserAgent is getAntigravityUserAgent.
func UserAgent() string {
	cl := envOr("PI_AI_ANTIGRAVITY_CL", DefaultChangelist)
	hostOS, hostArch := Platform()
	return fmt.Sprintf("antigravity/hub/%s (aidev_client; os_type=%s; arch=%s; cl=%s)", Version(),
		envOr("PI_AI_ANTIGRAVITY_OS", hostOS), envOr("PI_AI_ANTIGRAVITY_ARCH", hostArch), cl)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// SetClientHeaders is getAntigravityHeaders on a Go request as Bun's fetch
// frames it: the native client's user agent, gzip as the only encoding, and
// the `Accept: */*` and `Connection: keep-alive` Bun always adds. userAgent
// may override the default.
func SetClientHeaders(header http.Header, userAgent string) {
	if userAgent == "" {
		userAgent = UserAgent()
	}
	header.Set("User-Agent", userAgent)
	header.Set("Accept-Encoding", "gzip")
	header.Set("Accept", "*/*")
	header.Set("Connection", "keep-alive")
}
