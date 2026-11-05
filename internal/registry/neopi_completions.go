package registry

import (
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"sync"
)

// NeoPi's bundled catalog for each provider NeoProxy serves through NeoPi's
// OpenAI Chat Completions provider, exported by
// runtime/executor/testdata/capture_neopi_completions.ts: every row with the
// compat NeoPi resolves for it (deduplicated), plus the compat NeoPi derives
// for a discovered id the bundle does not hold.
//
//go:embed models/neopi_completions/*.json.gz
var neopiCompletionsCatalogFiles embed.FS

// NeoPiCompletionsCatalog is one provider's exported catalog.
type NeoPiCompletionsCatalog struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"baseUrl"`
	// Compats are the resolved completions compats the rows share.
	Compats []*NeoPiCompletionsCompat `json:"compats"`
	// UnknownCompatIndex is the compat NeoPi resolves for a non-bundled id.
	UnknownCompatIndex int `json:"unknownCompatIndex"`
	Models             []struct {
		ID            string            `json:"id"`
		Name          string            `json:"name"`
		API           string            `json:"api"`
		Provider      string            `json:"provider"`
		BaseURL       string            `json:"baseUrl"`
		Reasoning     bool              `json:"reasoning"`
		Input         []string          `json:"input"`
		ContextWindow int               `json:"contextWindow"`
		MaxTokens     int               `json:"maxTokens"`
		Thinking      *NeoPiThinking    `json:"thinking"`
		Headers       map[string]string `json:"headers"`
		CompatIndex   int               `json:"completionsCompatIndex"`
	} `json:"models"`
}

var (
	neopiCompletionsOnce    sync.Once
	neopiCompletionsCatalog map[string]*NeoPiCompletionsCatalog
	neopiCompletionsRows    map[string][]*NeoPiModel
	neopiCompletionsLoadErr error
)

func loadNeoPiCompletionsCatalogs() {
	catalogs := map[string]*NeoPiCompletionsCatalog{}
	rows := map[string][]*NeoPiModel{}
	entries, err := fs.ReadDir(neopiCompletionsCatalogFiles, "models/neopi_completions")
	if err != nil {
		neopiCompletionsLoadErr = err
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json.gz") {
			continue
		}
		data, readErr := readGzipFile("models/neopi_completions/" + name)
		if readErr != nil {
			neopiCompletionsLoadErr = readErr
			return
		}
		catalog := &NeoPiCompletionsCatalog{}
		if unmarshalErr := json.Unmarshal(data, catalog); unmarshalErr != nil {
			neopiCompletionsLoadErr = fmt.Errorf("registry: %s: %w", name, unmarshalErr)
			return
		}
		catalogs[catalog.Provider] = catalog
		providerRows := make([]*NeoPiModel, 0, len(catalog.Models))
		for i := range catalog.Models {
			row := catalog.Models[i]
			compat := catalog.UnknownCompat()
			if row.CompatIndex >= 0 && row.CompatIndex < len(catalog.Compats) {
				compat = catalog.Compats[row.CompatIndex]
			}
			providerRows = append(providerRows, &NeoPiModel{
				ID:                row.ID,
				Name:              row.Name,
				API:               row.API,
				Provider:          row.Provider,
				BaseURL:           row.BaseURL,
				Reasoning:         row.Reasoning,
				Input:             row.Input,
				ContextWindow:     row.ContextWindow,
				MaxTokens:         row.MaxTokens,
				Thinking:          row.Thinking,
				CompletionsCompat: compat,
			})
		}
		rows[catalog.Provider] = providerRows
	}
	neopiCompletionsCatalog, neopiCompletionsRows = catalogs, rows
}

func readGzipFile(name string) ([]byte, error) {
	file, err := neopiCompletionsCatalogFiles.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(reader)
}

// UnknownCompat is the compat NeoPi resolves for an id the bundle does not
// hold, which a live model listing can then add.
func (c *NeoPiCompletionsCatalog) UnknownCompat() *NeoPiCompletionsCompat {
	if c == nil || len(c.Compats) == 0 {
		return &NeoPiCompletionsCompat{}
	}
	if c.UnknownCompatIndex >= 0 && c.UnknownCompatIndex < len(c.Compats) {
		return c.Compats[c.UnknownCompatIndex]
	}
	return c.Compats[0]
}

// NeoPiCompletionsProvider reports whether provider has an exported catalog.
func NeoPiCompletionsProvider(provider string) (*NeoPiCompletionsCatalog, bool) {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	if neopiCompletionsLoadErr != nil {
		return nil, false
	}
	catalog, ok := neopiCompletionsCatalog[provider]
	return catalog, ok
}

// NeoPiCompletionsProviders lists the providers with an exported catalog.
func NeoPiCompletionsProviders() []string {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	out := make([]string, 0, len(neopiCompletionsCatalog))
	for provider := range neopiCompletionsCatalog {
		out = append(out, provider)
	}
	slices.Sort(out)
	return out
}

// NeoPiCompletionsBundledModels is the provider's bundled roster as registry
// models, each carrying the NeoPi record its executor shapes requests from.
func NeoPiCompletionsBundledModels(provider, ownedBy string) []*ModelInfo {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	out := make([]*ModelInfo, 0, len(neopiCompletionsRows[provider]))
	for _, row := range neopiCompletionsRows[provider] {
		out = append(out, NeoPiCompletionsModelInfo(row, ownedBy))
	}
	return out
}

// NeoPiCompletionsModel resolves a provider's model id to NeoPi's record; nil
// when the provider is unknown entirely, and a discovered row (unknown id)
// carrying NeoPi's default compat for the provider when the bundle lacks it.
func NeoPiCompletionsModel(provider, id string) *NeoPiModel {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	for _, row := range neopiCompletionsRows[provider] {
		if row.ID == id {
			return row.Clone()
		}
	}
	catalog, ok := neopiCompletionsCatalog[provider]
	if !ok {
		return nil
	}
	compat := catalog.UnknownCompat().Clone()
	return &NeoPiModel{ID: id, Name: id, API: "openai-completions", Provider: provider, BaseURL: catalog.BaseURL,
		Input: []string{"text"}, CompletionsCompat: compat}
}

// NeoPiCompletionsModelInfo materializes a NeoPi record as a registered model.
func NeoPiCompletionsModelInfo(row *NeoPiModel, ownedBy string) *ModelInfo {
	model := row.Clone()
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: ownedBy, Type: "openai-compatibility",
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
	if model.Reasoning && model.Thinking != nil {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: true}
	}
	return info
}

// NeoPiCompletionsMergedModels is NeoPi's model manager result: the bundled
// roster, with a live listing's ids added and its limits applied where the
// endpoint reports them. A listing only describes the chat roster, so a
// bundled row it omits stays (NeoPi keeps its static seed for those hosts).
func NeoPiCompletionsMergedModels(provider, ownedBy string, live []NeoPiDiscoveredModel) []*ModelInfo {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	if len(live) == 0 {
		return NeoPiCompletionsBundledModels(provider, ownedBy)
	}
	byID := map[string]NeoPiDiscoveredModel{}
	for _, model := range live {
		byID[model.ID] = model
	}
	rows := neopiCompletionsRows[provider]
	out := make([]*ModelInfo, 0, len(rows)+len(live))
	seen := make(map[string]bool, len(rows)+len(live))
	for _, row := range rows {
		merged := row.Clone()
		if discovered, ok := byID[merged.ID]; ok {
			seen[merged.ID] = true
			if discovered.Name != "" && discovered.Name != merged.ID {
				merged.Name = discovered.Name
			}
			if discovered.ContextWindow > 0 {
				merged.ContextWindow = discovered.ContextWindow
			}
			if discovered.MaxTokens > 0 {
				merged.MaxTokens = discovered.MaxTokens
			}
			if len(discovered.Input) > 0 {
				merged.Input = discovered.Input
			}
		}
		out = append(out, NeoPiCompletionsModelInfo(merged, ownedBy))
	}
	catalog, _ := NeoPiCompletionsProvider(provider)
	for _, discovered := range live {
		if seen[discovered.ID] {
			continue
		}
		row := &NeoPiModel{ID: discovered.ID, Name: discovered.Name, API: "openai-completions", Provider: provider,
			BaseURL: catalog.BaseURL, Reasoning: false, Input: []string{"text"},
			ContextWindow: discovered.ContextWindow, MaxTokens: discovered.MaxTokens,
			CompletionsCompat: catalog.UnknownCompat().Clone()}
		if row.Name == "" {
			row.Name = discovered.ID
		}
		out = append(out, NeoPiCompletionsModelInfo(row, ownedBy))
	}
	return out
}

// NeoPiDiscoveredModel is one id a provider's live model listing reported.
type NeoPiDiscoveredModel struct {
	ID            string
	Name          string
	ContextWindow int
	MaxTokens     int
	Input         []string
}
