package neopidialect

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

// Dialect is NeoPi's in-band dialect id (catalog identity/dialect.ts).
type Dialect string

const (
	DialectGLM       Dialect = "glm"
	DialectHermes    Dialect = "hermes"
	DialectKimi      Dialect = "kimi"
	DialectXML       Dialect = "xml"
	DialectAnthropic Dialect = "anthropic"
	DialectDeepSeek  Dialect = "deepseek"
	DialectHarmony   Dialect = "harmony"
	DialectQwen3     Dialect = "qwen3"
	DialectGemini    Dialect = "gemini"
	DialectGemma     Dialect = "gemma"
	DialectMiniMax   Dialect = "minimax"
)

// fallbackDialect is NeoPi's FALLBACK_DIALECT.
const fallbackDialect = DialectXML

//go:embed taxonomy_rules.json
var taxonomyRulesJSON []byte

type taxonomyMatcher struct {
	Kind    string `json:"kind"`
	Token   string `json:"token"`
	Bounded bool   `json:"bounded"`
}

type taxonomyOverride struct {
	Model       *string `json:"model"`
	Glob        *string `json:"glob"`
	Provider    *string `json:"provider"`
	Logical     *string `json:"logical"`
	Class       *string `json:"class"`
	ExpiresAtMs *int64  `json:"expiresAtMs"`
}

type taxonomyClass struct {
	ID        string             `json:"id"`
	Matchers  []taxonomyMatcher  `json:"matchers"`
	Overrides []taxonomyOverride `json:"overrides"`
}

type collapseSuffix struct {
	Suffix           string  `json:"suffix"`
	ExceptBarePrefix *string `json:"exceptBarePrefix"`
}

type taxonomyRules struct {
	Classes  []taxonomyClass  `json:"classes"`
	Suffixes []collapseSuffix `json:"suffixes"`

	// exact indexes provider-agnostic overrides that name a model, keyed by the
	// lowercase model; the first declaration wins. Provider-scoped overrides never
	// apply because dialect selection classifies with an empty provider.
	exact map[string]*taxonomyOverride
	// patterns lists provider-agnostic glob overrides in declaration order.
	patterns []patternOverride
}

type patternOverride struct {
	override    *taxonomyOverride
	glob        string
	specificity int
}

var (
	taxonomyOnce sync.Once
	taxonomy     *taxonomyRules
)

func loadTaxonomy() *taxonomyRules {
	taxonomyOnce.Do(func() {
		rules := &taxonomyRules{exact: map[string]*taxonomyOverride{}}
		if err := json.Unmarshal(taxonomyRulesJSON, rules); err != nil {
			panic("neopidialect: embedded taxonomy_rules.json is invalid: " + err.Error())
		}
		for ci := range rules.Classes {
			cls := &rules.Classes[ci]
			for oi := range cls.Overrides {
				override := &cls.Overrides[oi]
				if override.Provider != nil {
					continue
				}
				if override.Model == nil {
					if override.Glob != nil {
						rules.patterns = append(rules.patterns, patternOverride{
							override:    override,
							glob:        *override.Glob,
							specificity: nonWildcardBytes(*override.Glob),
						})
					}
					continue
				}
				key := jsLower(*override.Model)
				if _, ok := rules.exact[key]; !ok {
					rules.exact[key] = override
				}
			}
		}
		taxonomy = rules
	})
	return taxonomy
}

// PreferredDialect mirrors NeoPi's preferredDialect(modelId): classify the model
// id (empty provider, lenient) and map its class to a dialect.
func PreferredDialect(modelID string) Dialect {
	switch classifyModelClass(modelID) {
	case "anthropic":
		return DialectAnthropic
	case "glm":
		return DialectGLM
	case "gemini":
		return DialectGemini
	case "gemma":
		return DialectGemma
	case "kimi":
		return DialectKimi
	case "qwen":
		return DialectQwen3
	case "deepseek":
		return DialectDeepSeek
	case "minimax":
		return DialectMiniMax
	case "openai", "gpt-oss":
		return DialectHarmony
	default:
		return fallbackDialect
	}
}

func bareOf(id string) string {
	if slash := strings.LastIndexByte(id, '/'); slash != -1 {
		return id[slash+1:]
	}
	return id
}

// classifyModelClass is classifyModel("", modelId, { lenient: true }).class from
// NeoPi's catalog compat/taxonomy.ts.
func classifyModelClass(modelID string) string {
	rules := loadTaxonomy()
	trimmed := jsTrim(modelID)
	if override := rules.findOverride(bareOf(trimmed)); override != nil {
		logical := trimmed
		if override.Logical != nil {
			logical = *override.Logical
		}
		if override.Class != nil {
			return *override.Class
		}
		return rules.classifyRanks(logical)
	}
	logical := trimmed
	if len(trimmed) == len(modelID) {
		logical = rules.collapseLogicalID(trimmed)
	}
	return rules.classifyRanks(logical)
}

// findOverride is findIdentityOverride with an empty provider and lenient
// ambiguity handling. Expiring overrides stay active because dialect selection
// passes no observation time.
func (r *taxonomyRules) findOverride(bareModel string) *taxonomyOverride {
	lower := jsLower(bareModel)
	if override, ok := r.exact[lower]; ok {
		return override
	}
	var winner *patternOverride
	tied := false
	for i := range r.patterns {
		pattern := &r.patterns[i]
		if !globMatch(pattern.glob, lower) {
			continue
		}
		if winner != nil && winner.specificity == pattern.specificity {
			tied = true
		} else if winner == nil || winner.specificity < pattern.specificity {
			winner = pattern
			tied = false
		}
	}
	if tied || winner == nil {
		return nil
	}
	return winner.override
}

// collapseLogicalID is collapseVariantId("", model).logicalId. The effort-family
// aliases and provider lanes only apply to named providers, so only the suffix
// vocabulary can fire.
func (r *taxonomyRules) collapseLogicalID(model string) string {
	lower := jsLower(model)
	bare := bareOf(lower)
	var winner *collapseSuffix
	for i := range r.Suffixes {
		rule := &r.Suffixes[i]
		if !strings.HasSuffix(lower, rule.Suffix) {
			continue
		}
		if rule.ExceptBarePrefix != nil && strings.HasPrefix(bare, *rule.ExceptBarePrefix) {
			continue
		}
		if winner == nil || len(rule.Suffix) > len(winner.Suffix) {
			winner = rule
		}
	}
	if winner == nil {
		return model
	}
	return model[:len(model)-len(winner.Suffix)]
}

func matcherRank(kind string) int {
	switch kind {
	case "exact":
		return 4
	case "bounded":
		return 3
	case "namespace":
		return 2
	case "prefix":
		return 1
	default:
		return 0
	}
}

// classifyRanks is classifyRanks(model, lenient=true).class: matchers rank
// exact > bounded > namespace > prefix > glob with the token length as tiebreak;
// an ambiguous tie between two classes resolves to "unknown".
func (r *taxonomyRules) classifyRanks(model string) string {
	lower := jsLower(jsTrim(model))
	bare := bareOf(lower)
	var winner *taxonomyClass
	var winnerRank [2]int
	tied := false
	for ci := range r.Classes {
		cls := &r.Classes[ci]
		for _, matcher := range cls.Matchers {
			if !matcherMatches(matcher, lower, bare) {
				continue
			}
			rank := [2]int{matcherRank(matcher.Kind), len(matcher.Token)}
			if winner != nil && winnerRank == rank && winner.ID != cls.ID {
				tied = true
			} else if winner == nil || winnerRank[0] < rank[0] || (winnerRank[0] == rank[0] && winnerRank[1] < rank[1]) {
				winner = cls
				winnerRank = rank
				tied = false
			}
		}
	}
	if tied || winner == nil {
		return "unknown"
	}
	return winner.ID
}

func boundedMatch(value, token string) bool {
	if value == token {
		return true
	}
	if !strings.HasPrefix(value, token) || len(value) == len(token) {
		return false
	}
	next := value[len(token)]
	return next == '-' || next == '_' || next == '.' || next == ':' || (next >= '0' && next <= '9')
}

func matcherMatches(matcher taxonomyMatcher, lower, bare string) bool {
	switch matcher.Kind {
	case "exact":
		return bare == matcher.Token
	case "bounded":
		return boundedMatch(bare, matcher.Token)
	case "namespace":
		start := 0
		for start <= len(lower) {
			end := start
			for end < len(lower) {
				c := lower[end]
				if c == '/' || (matcher.Bounded && (c == '.' || c == ':')) {
					break
				}
				end++
			}
			part := lower[start:end]
			if part != "" {
				if matcher.Bounded {
					if boundedMatch(part, matcher.Token) {
						return true
					}
				} else if part == matcher.Token {
					return true
				}
			}
			if end == len(lower) {
				break
			}
			start = end + 1
		}
		return false
	case "prefix":
		return strings.HasPrefix(bare, matcher.Token)
	case "glob":
		return globMatch(matcher.Token, bare)
	}
	return false
}

func nonWildcardBytes(glob string) int {
	return len(glob) - strings.Count(glob, "*")
}

// globMatch is an anchored `*`-wildcard match; both sides must be pre-lowercased
// (NeoPi catalog compat/cascade.ts globMatch).
func globMatch(pattern, value string) bool {
	firstStar := strings.IndexByte(pattern, '*')
	if firstStar == -1 {
		return value == pattern
	}
	head := pattern[:firstStar]
	if !strings.HasPrefix(value, head) {
		return false
	}
	remainder := value[len(head):]
	pos := firstStar + 1
	for {
		nextStar := strings.IndexByte(pattern[pos:], '*')
		if nextStar == -1 {
			break
		}
		end := pos + nextStar
		segment := pattern[pos:end]
		if segment != "" {
			found := strings.Index(remainder, segment)
			if found == -1 {
				return false
			}
			remainder = remainder[found+len(segment):]
		}
		pos = end + 1
	}
	last := pattern[pos:]
	return last == "" || strings.HasSuffix(remainder, last)
}
