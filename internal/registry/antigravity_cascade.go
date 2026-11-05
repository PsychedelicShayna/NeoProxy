package registry

import (
	"fmt"
	"strings"
)

// This file ports NeoPi's compat/cascade.ts (the compiled rule cascade) and
// the google-antigravity subset of compat/resolve.ts plus build.ts's
// buildModel: identity, Google compat record, thinking policy and the catalog
// corrections that shape an Antigravity row.

const (
	antigravityProvider = "google-antigravity"
	antigravityAPI      = "google-gemini-cli"
)

// antigravityTarget is ResolveTarget.
type antigravityTarget struct {
	Class     string
	Family    string
	Revision  string
	Model     string
	Reasoning bool
}

// antigravityAxes is ResolvedAxes: the winning rule value per axis.
type antigravityAxes struct {
	Wire      map[string]any
	Thinking  map[string]any
	Catalog   map[string]any
	Reasoning bool
}

type antigravityIndexedRule struct {
	rule         *antigravityRule
	revision     []antigravityRevisionTerm
	hasRevision  bool
	priority     int
	dimensions   int
	order        int
	exactEfforts bool
}

type antigravityRuleIndex struct {
	globals         []antigravityIndexedRule
	byClass         map[string][]antigravityIndexedRule
	byProvider      map[string][]antigravityIndexedRule
	byClassProvider map[string]map[string][]antigravityIndexedRule
}

var antigravityRuleIndexOnce = func() *antigravityRuleIndex {
	index := &antigravityRuleIndex{
		byClass:         map[string][]antigravityIndexedRule{},
		byProvider:      map[string][]antigravityIndexedRule{},
		byClassProvider: map[string]map[string][]antigravityIndexedRule{},
	}
	for order, rule := range antigravityRules.Cascade {
		pointer := &antigravityRules.Cascade[order]
		indexed := antigravityIndexedRule{rule: pointer, order: order, priority: rule.Priority}
		if rule.Revision != nil {
			terms, ok := parseAntigravityRevisionConstraint(revisionTermsExpression(rule.Revision))
			if !ok {
				panic(fmt.Sprintf("registry: antigravity rule %s has an invalid revision constraint", rule.Source))
			}
			indexed.revision, indexed.hasRevision = terms, true
		}
		for _, dimension := range []bool{
			rule.Class != "", rule.Providers != nil, rule.APIs != nil,
			rule.Upstreams != nil, rule.Family != "", rule.Revision != nil, rule.Models != nil,
		} {
			if dimension {
				indexed.dimensions++
			}
		}
		indexed.exactEfforts = rule.Thinking != nil && rule.Thinking.Efforts != nil
		switch {
		case rule.Class == "" && rule.Providers == nil:
			index.globals = append(index.globals, indexed)
		case rule.Class == "":
			for _, provider := range uniqueStrings(rule.Providers) {
				index.byProvider[provider] = append(index.byProvider[provider], indexed)
			}
		case rule.Providers == nil:
			index.byClass[rule.Class] = append(index.byClass[rule.Class], indexed)
		default:
			providers := index.byClassProvider[rule.Class]
			if providers == nil {
				providers = map[string][]antigravityIndexedRule{}
				index.byClassProvider[rule.Class] = providers
			}
			for _, provider := range uniqueStrings(rule.Providers) {
				providers[provider] = append(providers[provider], indexed)
			}
		}
	}
	return index
}()

// revisionTermsExpression re-renders compiled revision terms as the constraint
// expression parseAntigravityRevisionConstraint reads.
func revisionTermsExpression(terms []antigravityRevisionTerm) string {
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		parts = append(parts, term.Op+term.Revision)
	}
	return strings.Join(parts, " ")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// antigravityRank is a rule's (selector exactness, constrained dimensions,
// priority) rank.
type antigravityRank [3]int

type antigravityRankedRule struct {
	indexed antigravityIndexedRule
	rank    antigravityRank
}

// antigravityRankRelevantRules is rankRelevantRules: rules from the four
// buckets that apply to the target, in rule order.
func antigravityRankRelevantRules(index *antigravityRuleIndex, target antigravityTarget) []antigravityRankedRule {
	buckets := [][]antigravityIndexedRule{
		index.globals,
		index.byClass[target.Class],
		index.byProvider[antigravityProvider],
		index.byClassProvider[target.Class][antigravityProvider],
	}
	positions := [4]int{}
	ranked := make([]antigravityRankedRule, 0, 16)
	for {
		nextBucket, nextOrder := -1, 0
		for bucket := range buckets {
			if positions[bucket] >= len(buckets[bucket]) {
				continue
			}
			if order := buckets[bucket][positions[bucket]].order; nextBucket < 0 || order < nextOrder {
				nextBucket, nextOrder = bucket, order
			}
		}
		if nextBucket < 0 {
			return ranked
		}
		indexed := buckets[nextBucket][positions[nextBucket]]
		positions[nextBucket]++
		if rank, ok := antigravityRankRule(indexed, target); ok {
			ranked = append(ranked, antigravityRankedRule{indexed: indexed, rank: rank})
		}
	}
}

// antigravityRankRule is rankRule.
func antigravityRankRule(indexed antigravityIndexedRule, target antigravityTarget) (antigravityRank, bool) {
	rule := indexed.rule
	if rule.APIs != nil && !slicesContains(rule.APIs, antigravityAPI) {
		return antigravityRank{}, false
	}
	if rule.Upstreams != nil {
		return antigravityRank{}, false
	}
	if rule.Family != "" && rule.Family != target.Family {
		return antigravityRank{}, false
	}
	if indexed.hasRevision {
		revision, ok := parseAntigravityRevision(target.Revision)
		if !ok || !antigravityRevisionSatisfies(revision, indexed.revision) {
			return antigravityRank{}, false
		}
	}
	exactness := 0
	if rule.Models != nil {
		best := -1
		lower := asciiLower(target.Model)
		tokens := antigravityModelTokens(lower)
		for _, selector := range rule.Models {
			var matched bool
			value := 1
			switch selector.Kind {
			case "exact":
				matched, value = selector.Value == target.Model, 2
			case "glob":
				matched = antigravityGlobMatch(selector.Value, lower)
			case "token":
				matched = slicesContains(tokens, selector.Value)
			}
			if matched && value > best {
				best = value
			}
		}
		if best < 0 {
			return antigravityRank{}, false
		}
		exactness = best
	}
	return antigravityRank{exactness, indexed.dimensions, indexed.priority}, true
}

// antigravityModelTokens is `modelLower.split(/[^a-z0-9]+/)`.
func antigravityModelTokens(lower string) []string {
	return strings.FieldsFunc(lower, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
}

func antigravityRankCompare(a, b antigravityRank) int {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// antigravityAxisWinners is the per-axis winning rule.
type antigravityAxisWinners map[string]antigravityRankedRule

// antigravityContestAxis is contestAxis: a strictly better rank takes the
// axis, an equal rank is an authoring defect NeoPi refuses to resolve.
func antigravityContestAxis(winners antigravityAxisWinners, axis string, ranked antigravityRankedRule) error {
	held, ok := winners[axis]
	if !ok {
		winners[axis] = ranked
		return nil
	}
	switch order := antigravityRankCompare(held.rank, ranked.rank); {
	case order == 0:
		return fmt.Errorf("antigravity: ambiguous overlap for %s on axis %q between rules %q and %q",
			antigravityProvider, axis, held.indexed.rule.Source, ranked.indexed.rule.Source)
	case order > 0:
		return nil
	}
	winners[axis] = ranked
	return nil
}

// antigravityContest is contest.
func antigravityContest(winners antigravityAxisWinners, axes map[string]any, ranked antigravityRankedRule) error {
	for axis := range axes {
		if err := antigravityContestAxis(winners, axis, ranked); err != nil {
			return err
		}
	}
	return nil
}

// antigravityThinkingAxes is the thinking axis of a rule as raw values.
func antigravityThinkingAxes(rule *antigravityThinkingRule) map[string]any {
	if rule == nil {
		return nil
	}
	out := map[string]any{}
	if rule.Mode != "" {
		out["mode"] = rule.Mode
	}
	if rule.Efforts != nil {
		out["efforts"] = rule.Efforts
	}
	if rule.DefaultLevel != "" {
		out["defaultLevel"] = rule.DefaultLevel
	}
	if rule.EffortMap != nil {
		out["effortMap"] = rule.EffortMap
	}
	if rule.EffortBudgets != nil {
		out["effortBudgets"] = rule.EffortBudgets
	}
	if rule.RequiresEffort != nil {
		out["requiresEffort"] = *rule.RequiresEffort
	}
	if rule.SuppressWhenOff != nil {
		out["suppressWhenOff"] = *rule.SuppressWhenOff
	}
	if rule.SupportsDisplay != nil {
		out["supportsDisplay"] = *rule.SupportsDisplay
	}
	if rule.PrefixBinding != nil {
		out["prefixBinding"] = *rule.PrefixBinding
	}
	if rule.UpgradeNeutral != nil {
		out["upgradeNeutral"] = *rule.UpgradeNeutral
	}
	if rule.Unverified != nil {
		out["unverified"] = *rule.Unverified
	}
	return out
}

// resolveAntigravityCascade is resolveCascade for a google-antigravity /
// google-gemini-cli target.
func resolveAntigravityCascade(target antigravityTarget) (antigravityAxes, error) {
	ranked := antigravityRankRelevantRules(antigravityRuleIndexOnce, target)
	reasoning := target.Reasoning
	if !reasoning {
		upgrade := antigravityAxisWinners{}
		hasEfforts := false
		for _, entry := range ranked {
			thinking := antigravityThinkingAxes(entry.indexed.rule.Thinking)
			if thinking == nil {
				continue
			}
			if _, ok := thinking["upgradeNeutral"]; ok {
				if err := antigravityContestAxis(upgrade, "upgradeNeutral", entry); err != nil {
					return antigravityAxes{}, err
				}
			}
			if _, ok := thinking["efforts"]; ok {
				hasEfforts = true
			}
			if entry.indexed.exactEfforts && entry.rank[0] == 2 {
				reasoning = true
			}
		}
		if winner, ok := upgrade["upgradeNeutral"]; ok {
			rule := winner.indexed.rule
			identityScoped := (rule.Class != "" && rule.Class != "unknown") ||
				rule.Family != "" || rule.Revision != nil || rule.Models != nil
			if identityScoped && hasEfforts {
				reasoning = true
			}
		}
	}
	// Unclassified targets get their provider's ladder too: it is offered as
	// unverified thinking, never as reasoning capability.
	offersThinking := reasoning || target.Class == "unknown"
	wire := antigravityAxisWinners{}
	thinking := antigravityAxisWinners{}
	catalog := antigravityAxisWinners{}
	for _, entry := range ranked {
		rule := entry.indexed.rule
		if err := antigravityContest(wire, rule.Wire, entry); err != nil {
			return antigravityAxes{}, err
		}
		if err := antigravityContest(catalog, rule.Catalog, entry); err != nil {
			return antigravityAxes{}, err
		}
		if offersThinking {
			if err := antigravityContest(thinking, antigravityThinkingAxes(rule.Thinking), entry); err != nil {
				return antigravityAxes{}, err
			}
		}
	}
	axes := antigravityAxes{Reasoning: reasoning}
	axes.Wire = antigravityCollectAxis(wire, func(rule *antigravityRule) map[string]any { return rule.Wire })
	axes.Thinking = antigravityCollectAxis(thinking, func(rule *antigravityRule) map[string]any {
		return antigravityThinkingAxes(rule.Thinking)
	})
	axes.Catalog = antigravityCollectAxis(catalog, func(rule *antigravityRule) map[string]any { return rule.Catalog })
	return axes, nil
}

// antigravityCollectAxis is collect: every contested axis takes the winning
// rule's own value.
func antigravityCollectAxis(winners antigravityAxisWinners, pick func(*antigravityRule) map[string]any) map[string]any {
	if len(winners) == 0 {
		return nil
	}
	out := make(map[string]any, len(winners))
	for axis, winner := range winners {
		out[axis] = pick(winner.indexed.rule)[axis]
	}
	return out
}
