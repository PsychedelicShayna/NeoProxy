package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// googleNeoPiJSON is NeoPi's bundled `google` catalog (Gemini API key
// provider), exported by runtime/executor/testdata/capture_gemini_npi_profiles.ts.
// NeoPi serves an account from it until that account's /v1beta/models listing
// answers, then overlays the listing on it (it never prunes).
//
//go:embed models/google_neopi.json
var googleNeoPiJSON []byte

// GoogleBaseURL is the Gemini API base NeoPi's google rows carry, version included.
const GoogleBaseURL = "https://generativelanguage.googleapis.com/v1beta"

var googleBundledCatalog = func() []NeoPiModel {
	var export struct {
		Models []NeoPiModel `json:"models"`
	}
	if err := json.Unmarshal(googleNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: google_neopi.json: %v", err))
	}
	return export.Models
}()

// GoogleBundledModels is NeoPi's bundled google catalog as registry models.
func GoogleBundledModels() []*ModelInfo {
	out := make([]*ModelInfo, 0, len(googleBundledCatalog))
	for i := range googleBundledCatalog {
		out = append(out, googleModelInfo(&googleBundledCatalog[i]))
	}
	return out
}

// GoogleBundledNeoPiModel resolves an id against NeoPi's bundled google
// catalog; nil when the bundle does not carry it.
func GoogleBundledNeoPiModel(id string) *NeoPiModel {
	for i := range googleBundledCatalog {
		if googleBundledCatalog[i].ID == id {
			return googleBundledCatalog[i].Clone()
		}
	}
	return nil
}

// GoogleNeoPiModelFor is the NeoPi record for a model id the catalog may not
// carry: the bundled row, else the row NeoPi's compat rules would build for a
// discovered id with no reference.
func GoogleNeoPiModelFor(id string) *NeoPiModel {
	if model := GoogleBundledNeoPiModel(id); model != nil {
		return model
	}
	model := GoogleBuildDiscovered(id, id, 0, 0)
	return &model
}

func googleModelInfo(model *NeoPiModel) *ModelInfo {
	info := &ModelInfo{ID: model.ID, Object: "model", OwnedBy: "google", Type: "gemini", Name: "models/" + model.ID, DisplayName: model.Name,
		ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model.Clone()}
	if model.Reasoning && model.Thinking != nil {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort || model.Thinking.SuppressWhenOff}
	}
	return info
}

// GoogleModels maps the "models" entries of a /v1beta/models listing (every
// page merged by the caller) to the registry models NeoPi serves: its bundled
// rows with the listing overlaid.
func GoogleModels(listing []gjson.Result) []*ModelInfo {
	merged := map[string]NeoPiModel{}
	var order []string
	for i := range googleBundledCatalog {
		merged[googleBundledCatalog[i].ID] = *googleBundledCatalog[i].Clone()
		order = append(order, googleBundledCatalog[i].ID)
	}
	discovered := map[string]NeoPiModel{}
	for _, item := range listing {
		model, ok := GoogleDiscoveredModel(item)
		if ok {
			discovered[model.ID] = model
		}
	}
	ids := make([]string, 0, len(discovered))
	for id := range discovered {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		dynamic := discovered[id]
		existing, ok := merged[id]
		if !ok {
			merged[id] = dynamic
			order = append(order, id)
			continue
		}
		// mergeDynamicModel: the live name wins unless it is the bare id, a live
		// limit wins unless absent (or the 4096 placeholder), images and
		// reasoning are additive.
		if name := strings.TrimSpace(dynamic.Name); name != "" && !(name == id && existing.Name != id) {
			existing.Name = name
		}
		existing.ContextWindow = googlePreferLimit(dynamic.ContextWindow, existing.ContextWindow)
		existing.MaxTokens = googlePreferLimit(dynamic.MaxTokens, existing.MaxTokens)
		if slices.Contains(dynamic.Input, "image") && !slices.Contains(existing.Input, "image") {
			existing.Input = []string{"text", "image"}
		}
		merged[id] = existing
	}
	out := make([]*ModelInfo, 0, len(order))
	for _, id := range order {
		model := merged[id]
		out = append(out, googleModelInfo(&model))
	}
	return out
}

// GoogleDiscoveredModel is discovery's normalizeModel for one listing entry:
// a row on its bundled reference when there is one, else built from the id.
// ok is false for entries that cannot generate content.
func GoogleDiscoveredModel(item gjson.Result) (NeoPiModel, bool) {
	id := strings.TrimPrefix(strings.TrimSpace(item.Get("name").String()), "models/")
	if id == "" || !googleSupportsGenerateContent(item.Get("supportedGenerationMethods")) {
		return NeoPiModel{}, false
	}
	reference := GoogleBundledNeoPiModel(id)
	var refContext, refMax int
	if reference != nil {
		refContext, refMax = reference.ContextWindow, reference.MaxTokens
	}
	contextWindow := googlePositive(item.Get("inputTokenLimit"), refContext)
	maxTokens := googlePositive(item.Get("outputTokenLimit"), refMax)
	name := strings.TrimSpace(item.Get("displayName").String())
	if reference != nil {
		if name == "" {
			name = reference.Name
		}
		model := *reference
		model.Name, model.ContextWindow, model.MaxTokens = name, contextWindow, maxTokens
		return model, true
	}
	if name == "" {
		name = id
	}
	return GoogleBuildDiscovered(id, name, contextWindow, maxTokens), true
}

func googleSupportsGenerateContent(methods gjson.Result) bool {
	for _, method := range methods.Array() {
		if method.String() == "generateContent" {
			return true
		}
	}
	return false
}

func googlePositive(value gjson.Result, fallback int) int {
	if value.Type == gjson.Number && value.Float() > 0 && !math.IsInf(value.Float(), 0) {
		return int(math.Floor(value.Float()))
	}
	return fallback
}

func googlePreferLimit(discovery, fallback int) int {
	if discovery <= 0 {
		return fallback
	}
	if discovery == 4096 && fallback > discovery {
		return fallback
	}
	return discovery
}

var googleRevisionPattern = regexp.MustCompile(`^gemini-(\d+)(?:\.(\d+))?(?:-|$)`)

// googleRevision is the Gemini generation of an id; ok is false for ids
// without one (gemini-pro-vision, gemini-exp-1206).
func googleRevision(id string) (float64, bool) {
	match := googleRevisionPattern.FindStringSubmatch(id)
	if match == nil {
		return 0, false
	}
	minor := match[2]
	if minor == "" {
		minor = "0"
	}
	value, _ := strconv.ParseFloat(match[1]+"."+minor, 64)
	return value, true
}

// GoogleBuildDiscovered is discovery's row for an id with no bundled
// reference, resolved the way NeoPi's buildModel applies its
// google-generative-ai compat rules (rules/classes/gemini.kdl). The
// conformance test pins it to NeoPi's own resolution of a probe listing.
func GoogleBuildDiscovered(id, name string, contextWindow, maxTokens int) NeoPiModel {
	lower := strings.ToLower(id)
	reasoning := strings.Contains(lower, "thinking") || strings.Contains(lower, "pro") || strings.Contains(lower, "2.5")
	input := []string{"text"}
	if strings.Contains(lower, "vision") || strings.Contains(lower, "image") || strings.Contains(lower, "gemini") {
		input = []string{"text", "image"}
	}
	model := NeoPiModel{ID: id, Name: name, API: "google-generative-ai", Provider: "google", BaseURL: GoogleBaseURL,
		Reasoning: reasoning, Input: input, ContextWindow: contextWindow, MaxTokens: maxTokens}
	revision, hasRevision := googleRevision(lower)
	isGemini := strings.HasPrefix(lower, "gemini")
	argon := lower == "gemini-4-argon" || lower == "gemini-4-argon-cyber-permissive"
	// Gemini 4 Argon is repaired to a selectable ladder although its id infers no reasoning.
	if argon {
		model.Reasoning = true
	}
	compat := NeoPiGoogleCompat{}
	if isGemini && hasRevision && revision >= 3 {
		compat.MultimodalFunctionResponse = true
		compat.RequiresSkipThoughtSignature = true
		compat.SupportsFunctionPartID = true
	}
	model.GoogleCompat = &compat
	if !model.Reasoning {
		return model
	}
	thinking := &NeoPiThinking{Efforts: []string{"minimal", "low", "medium", "high"}, Mode: "budget"}
	if isGemini {
		family := ""
		switch {
		case strings.Contains(lower, "-lite"):
			family = "lite"
		case strings.Contains(lower, "flash"):
			family = "flash"
		case strings.Contains(lower, "pro"):
			family = "pro"
		}
		in := func(lo, hi float64) bool { return hasRevision && revision >= lo && revision < hi }
		if family == "pro" && hasRevision && revision >= 3 {
			thinking.Efforts = []string{"low", "high"}
		}
		if family == "pro" && in(2.5, 3) {
			thinking.RequiresEffort = true
		}
		if family == "flash" && hasRevision && revision >= 3.7 {
			thinking.Efforts = []string{"low", "medium", "high"}
		}
		if argon {
			thinking.Efforts = []string{"low", "medium", "high"}
		}
		if in(2.5, 2.6) {
			switch family {
			case "pro":
				thinking.EffortBudgets = map[string]int{"minimal": 128, "low": 2048, "medium": 8192, "high": 32768, "xhigh": 32768, "max": 32768}
			case "flash", "lite":
				thinking.EffortBudgets = map[string]int{"minimal": 128, "low": 2048, "medium": 8192, "high": 24576, "xhigh": 24576, "max": 24576}
			}
		}
		if hasRevision && revision >= 3 {
			thinking.RequiresEffort = true
			thinking.Mode = "google-level"
		}
		if strings.Contains(lower, "-thinking") {
			thinking.RequiresEffort = true
		}
	}
	model.Thinking = thinking
	return model
}
