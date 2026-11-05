package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// antigravityRulesJSON is the slice of NeoPi's compiled rule tree a
// google-antigravity row resolves through, exported by
// testdata/capture_antigravity_npi_rules.ts.
//
//go:embed models/antigravity_rules.json
var antigravityRulesJSON []byte

// antigravityClass is CompiledClass: one taxonomy class and the matchers,
// families, revision prefixes and reviewed identity overrides that read it.
type antigravityClass struct {
	ID               string                      `json:"id"`
	Matchers         []antigravityMatcher        `json:"matchers"`
	Families         []antigravityTaxonomyFamily `json:"families"`
	RevisionPrefixes []antigravityRevPrefix      `json:"revisionPrefixes"`
	SkipBare         []string                    `json:"skipBare"`
	Overrides        []antigravityIdentityEntry  `json:"overrides"`
}

// antigravityMatcher is CompiledMatcher; bounded is set for namespace
// matchers that match per segment.
type antigravityMatcher struct {
	Kind    string `json:"kind"`
	Token   string `json:"token"`
	Bounded bool   `json:"bounded"`
}

// antigravityTaxonomyFamily is CompiledFamily: a glob that names a family
// inside a class, ranked by priority then glob specificity.
type antigravityTaxonomyFamily struct {
	ID       string `json:"id"`
	Glob     string `json:"glob"`
	Priority int    `json:"priority"`
}

// antigravityRevPrefix is CompiledRevisionPrefix: where a class's revision is
// read from in a bare model id.
type antigravityRevPrefix struct {
	Prefix   string `json:"prefix"`
	Anywhere bool   `json:"anywhere"`
}

// antigravityIdentityEntry is CompiledIdentityOverride.
type antigravityIdentityEntry struct {
	ID              string `json:"id"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	Glob            string `json:"glob,omitempty"`
	Logical         string `json:"logical,omitempty"`
	Class           string `json:"class,omitempty"`
	Family          string `json:"family,omitempty"`
	Revision        string `json:"revision,omitempty"`
	Effort          string `json:"effort,omitempty"`
	ThinkingVariant bool   `json:"thinkingVariant,omitempty"`
	ExpiresAtMs     *int64 `json:"expiresAtMs,omitempty"`
}

// antigravitySuffix is CompiledCollapseSuffix.
type antigravitySuffix struct {
	Suffix           string `json:"suffix"`
	Effort           string `json:"effort,omitempty"`
	Thinking         bool   `json:"thinking,omitempty"`
	ExceptBarePrefix string `json:"exceptBarePrefix,omitempty"`
}

// antigravityLane is CompiledEffortLane: a service lane some providers wedge
// an effort tier in front of.
type antigravityLane struct {
	Suffix     string   `json:"suffix"`
	Providers  []string `json:"providers"`
	BarePrefix string   `json:"barePrefix,omitempty"`
}

// antigravityEffortFamily is CompiledEffortFamily: provider-scoped aliases that
// collapse to a logical id without naming an effort.
type antigravityEffortFamily struct {
	Provider string   `json:"provider"`
	Logical  string   `json:"logical"`
	Aliases  []string `json:"aliases"`
}

// antigravityVariantFamily is CompiledVariantFamily: one reviewed row family
// whose members are per-effort wire ids.
type antigravityVariantFamily struct {
	Provider                   string            `json:"provider"`
	ID                         string            `json:"id"`
	Name                       string            `json:"name"`
	Revision                   string            `json:"revision,omitempty"`
	Members                    []string          `json:"members"`
	DefaultMember              string            `json:"defaultMember,omitempty"`
	RetiredMembers             []string          `json:"retiredMembers,omitempty"`
	Routing                    map[string]string `json:"routing"`
	EffortBudgets              map[string]int    `json:"effortBudgets,omitempty"`
	Mode                       string            `json:"mode,omitempty"`
	Efforts                    []string          `json:"efforts,omitempty"`
	DefaultLevel               string            `json:"defaultLevel,omitempty"`
	RequiresEffort             bool              `json:"requiresEffort,omitempty"`
	SuppressWhenOff            bool              `json:"suppressWhenOff,omitempty"`
	NoThinking                 bool              `json:"noThinking,omitempty"`
	PreserveAbsentEffortRoutes bool              `json:"preserveAbsentEffortRoutes,omitempty"`
	ExtraAliases               []string          `json:"extraAliases,omitempty"`
}

// antigravityRevisionTerm is one comparison of a compiled revision constraint.
type antigravityRevisionTerm struct {
	Op       string `json:"op"`
	Revision string `json:"revision"`
}

// antigravitySelector is CompiledSelector: a rule's model-scoped matcher.
type antigravitySelector struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// antigravityThinkingRule is the thinking axis of a compiled cascade rule.
type antigravityThinkingRule struct {
	Mode            string            `json:"mode,omitempty"`
	Efforts         []string          `json:"efforts,omitempty"`
	DefaultLevel    string            `json:"defaultLevel,omitempty"`
	EffortMap       map[string]string `json:"effortMap,omitempty"`
	EffortBudgets   map[string]int    `json:"effortBudgets,omitempty"`
	RequiresEffort  *bool             `json:"requiresEffort,omitempty"`
	SuppressWhenOff *bool             `json:"suppressWhenOff,omitempty"`
	SupportsDisplay *bool             `json:"supportsDisplay,omitempty"`
	PrefixBinding   *bool             `json:"prefixBinding,omitempty"`
	UpgradeNeutral  *bool             `json:"upgradeNeutral,omitempty"`
	Unverified      *bool             `json:"unverified,omitempty"`
}

// antigravityRule is CompiledRule: one conjunction of selectors and the axes
// it assigns.
type antigravityRule struct {
	Source    string                    `json:"source"`
	Class     string                    `json:"class,omitempty"`
	Providers []string                  `json:"providers,omitempty"`
	APIs      []string                  `json:"apis,omitempty"`
	Upstreams []string                  `json:"upstreams,omitempty"`
	Family    string                    `json:"family,omitempty"`
	Revision  []antigravityRevisionTerm `json:"revision,omitempty"`
	Models    []antigravitySelector     `json:"models,omitempty"`
	Priority  int                       `json:"priority,omitempty"`
	Wire      map[string]any            `json:"wire,omitempty"`
	Thinking  *antigravityThinkingRule  `json:"thinking,omitempty"`
	Catalog   map[string]any            `json:"catalog,omitempty"`
}

// antigravityExcludeMatch is CompiledMatchList: the ways an exclude-models
// rule names a roster id.
type antigravityExcludeMatch struct {
	Exact     []string `json:"exact,omitempty"`
	Prefix    []string `json:"prefix,omitempty"`
	Substring []string `json:"substring,omitempty"`
	Token     []string `json:"token,omitempty"`
	Glob      []string `json:"glob,omitempty"`
}

// antigravityExcludeRule is one `exclude-models provider="google-antigravity"` node.
type antigravityExcludeRule struct {
	Provider string                  `json:"provider"`
	Match    antigravityExcludeMatch `json:"match"`
}

// antigravityRulesDoc is the exported rule slice: the taxonomy, the cascade
// rules a google-antigravity / google-gemini-cli target can match, the compat
// keys the google record accepts, the reviewed variant families and the
// roster exclusions.
type antigravityRulesDoc struct {
	Taxonomy struct {
		Classes        []antigravityClass        `json:"classes"`
		Suffixes       []antigravitySuffix       `json:"suffixes"`
		PairTokens     []string                  `json:"pairTokens"`
		Lanes          []antigravityLane         `json:"lanes"`
		EffortFamilies []antigravityEffortFamily `json:"effortFamilies"`
	} `json:"taxonomy"`
	Cascade         []antigravityRule          `json:"cascade"`
	GoogleWireKeys  []string                   `json:"googleWireKeys"`
	VariantFamilies []antigravityVariantFamily `json:"variantFamilies"`
	ProviderAliases map[string]string          `json:"providerAliases"`
	ExcludeModels   []antigravityExcludeRule   `json:"excludeModels"`
}

var antigravityRules = func() *antigravityRulesDoc {
	var doc antigravityRulesDoc
	if err := json.Unmarshal(antigravityRulesJSON, &doc); err != nil {
		panic(fmt.Sprintf("registry: antigravity_rules.json: %v", err))
	}
	for i := range doc.Taxonomy.Classes {
		for j := range doc.Taxonomy.Classes[i].Overrides {
			entry := &doc.Taxonomy.Classes[i].Overrides[j]
			if entry.Provider != "" {
				entry.Provider = asciiLower(entry.Provider)
			}
			if entry.Model != "" {
				entry.Model = asciiLower(entry.Model)
			}
		}
	}
	return &doc
}()

// asciiLower is String.prototype.toLowerCase for the ASCII model ids,
// providers and rule tokens NeoPi compiles: it never changes a byte outside
// A-Z, so byte offsets into a lowercased id stay valid for the original.
func asciiLower(value string) string {
	for i := 0; i < len(value); i++ {
		if c := value[i]; c >= 'A' && c <= 'Z' {
			out := []byte(value)
			for j := i; j < len(out); j++ {
				if out[j] >= 'A' && out[j] <= 'Z' {
					out[j] += 'a' - 'A'
				}
			}
			return string(out)
		}
	}
	return value
}
