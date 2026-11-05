package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

// antigravityNeoPiJSON is NeoPi's bundled google-antigravity seed as NeoPiModel
// records, exported by testdata/capture_antigravity_npi_models.ts. An account
// serves it until its fetchAvailableModels roster answers, and NeoPi's model
// manager merges a live roster over it.
//
//go:embed models/antigravity_neopi.json
var antigravityNeoPiJSON []byte

// antigravityRow is one seed record plus the catalog kind only discovery and
// retention read.
type antigravityRow struct {
	NeoPiModel
	Kind string `json:"kind,omitempty"`
}

var antigravitySeedCatalog = func() []antigravityRow {
	var export struct {
		Models []antigravityRow `json:"models"`
	}
	if err := json.Unmarshal(antigravityNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: antigravity_neopi.json: %v", err))
	}
	return export.Models
}()

// antigravityBundledRows is the seed as discovery rows.
func antigravityBundledRows() ([]antigravityModel, error) {
	out := make([]antigravityModel, 0, len(antigravitySeedCatalog))
	for i := range antigravitySeedCatalog {
		row := &antigravitySeedCatalog[i]
		spec := antigravitySpec{
			ID:                row.ID,
			Name:              row.Name,
			BaseURL:           row.BaseURL,
			Reasoning:         row.Reasoning,
			Input:             slices.Clone(row.Input),
			ContextWindow:     row.ContextWindow,
			MaxTokens:         row.MaxTokens,
			RequestModelID:    row.RequestModelID,
			HasRequestModelID: row.RequestModelID != "",
			Thinking:          row.Thinking.Clone(),
			Kind:              row.Kind,
		}
		if len(row.AntigravityWireProfiles) > 0 {
			spec.WireProfiles = make(map[string]NeoPiAntigravityWireProfile, len(row.AntigravityWireProfiles))
			for wireID, profile := range row.AntigravityWireProfiles {
				if profile.ThinkingBudget != nil {
					budget := *profile.ThinkingBudget
					profile.ThinkingBudget = &budget
				}
				spec.WireProfiles[wireID] = profile
			}
		}
		model := antigravityModel{antigravitySpec: spec}
		if row.GeminiCompat != nil {
			model.Compat = *row.GeminiCompat
		}
		model.IdentityClass = antigravityClassifyModel(antigravityProvider, row.ID).Class
		out = append(out, model)
	}
	return out, nil
}

// AntigravityBundledModels is NeoPi's bundled google-antigravity seed as
// registry models: the chat rows an account serves until discovery answers,
// and the image rows NeoPi runs through generateAntigravityImage. Other
// non-chat SKUs are not served.
func AntigravityBundledModels() []*ModelInfo {
	out := make([]*ModelInfo, 0, len(antigravitySeedCatalog))
	for i := range antigravitySeedCatalog {
		row := &antigravitySeedCatalog[i]
		if row.Kind == antigravityImageKind {
			out = append(out, AntigravityImageModelInfo(row.NeoPiModel.Clone()))
			continue
		}
		if row.Kind != "" && row.Kind != "chat" {
			continue
		}
		out = append(out, AntigravityModelInfo(row.NeoPiModel.Clone()))
	}
	return out
}

// AntigravityBundledNeoPiModel resolves an id against NeoPi's bundled
// google-antigravity seed.
func AntigravityBundledNeoPiModel(id string) *NeoPiModel {
	for i := range antigravitySeedCatalog {
		row := &antigravitySeedCatalog[i]
		if row.ID != id || (row.Kind != "" && row.Kind != "chat") {
			continue
		}
		return row.NeoPiModel.Clone()
	}
	return nil
}

// AntigravityModelInfo is one NeoPi google-antigravity record as a registry
// model. Thinking support mirrors NeoPi's getSupportedThinkingLevels: the
// model's efforts, plus "off" unless the model must think.
func AntigravityModelInfo(model *NeoPiModel) *ModelInfo {
	if model == nil {
		return nil
	}
	info := &ModelInfo{
		ID: model.ID, Object: "model", OwnedBy: antigravityProvider, Type: "antigravity", Name: model.ID,
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow,
		MaxCompletionTokens: model.MaxTokens, SupportedInputModalities: slices.Clone(model.Input),
		NeoPi: model.Clone(),
	}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &ThinkingSupport{
			Levels:      slices.Clone(model.Thinking.Efforts),
			ZeroAllowed: !model.Thinking.RequiresEffort || model.Thinking.SuppressWhenOff,
		}
	}
	return info
}
