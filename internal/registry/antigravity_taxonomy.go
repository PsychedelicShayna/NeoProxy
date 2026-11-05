package registry

import (
	"strconv"
	"strings"
)

// This file ports NeoPi's pi-catalog compat/taxonomy.ts (classifyModel and the
// collapse vocabulary), compat/cascade.ts (globMatch), compat/revision.ts and
// the revision helpers the Antigravity rule tree reads.

// antigravityIdentity is ModelIdentity: a wire id's structured class, family
// and revision.
type antigravityIdentity struct {
	Class           string
	Family          string
	Revision        string
	Effort          string
	ThinkingVariant bool
	LogicalID       string
}

// antigravityRevision is a parsed major.minor.patch revision.
type antigravityRevision [3]int

// ParseAntigravityRevision is parseRevision: one to three components split on
// `.` or `-`, each an unsigned 8-bit value.
func parseAntigravityRevision(value string) (antigravityRevision, bool) {
	var out antigravityRevision
	count := 0
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '.' || r == '-' }) {
		if count == 3 {
			return out, false
		}
		component, ok := parseAntigravityRevisionComponent(part)
		if !ok {
			return out, false
		}
		out[count] = component
		count++
	}
	return out, count > 0
}

func parseAntigravityRevisionComponent(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	out := 0
	for i := 0; i < len(value); i++ {
		if code := value[i]; code < '0' || code > '9' {
			return 0, false
		}
		out = out*10 + int(value[i]-'0')
		if out > 255 {
			return 0, false
		}
	}
	return out, true
}

// parseAntigravityRevisionPrefix is parseRevisionPrefix: the leading revision
// of an identifier tail, stopping at a component that runs into a letter (a
// parameter count or size token, never a revision).
func parseAntigravityRevisionPrefix(value string) (antigravityRevision, bool) {
	var out antigravityRevision
	count := 0
	index := 0
	for count < 3 {
		start := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		sizeToken := index < len(value) && isAntigravityLetter(value[index])
		component := 0
		ok := false
		if !sizeToken {
			component, ok = parseAntigravityRevisionComponent(value[start:index])
		}
		if !ok {
			return out, count > 0
		}
		out[count] = component
		count++
		if index >= len(value) {
			break
		}
		separator := value[index]
		next := byte(0)
		if index+1 < len(value) {
			next = value[index+1]
		}
		if (separator != '.' && separator != '-') || next < '0' || next > '9' {
			break
		}
		index++
	}
	return out, true
}

func isAntigravityLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func compareAntigravityRevision(a, b antigravityRevision) int {
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

// formatAntigravityRevision renders a revision as `major.minor.patch`.
func formatAntigravityRevision(rev antigravityRevision) string {
	return strconv.Itoa(rev[0]) + "." + strconv.Itoa(rev[1]) + "." + strconv.Itoa(rev[2])
}

// parseAntigravityRevisionConstraint is parseRevisionConstraint: a
// whitespace-separated conjunction of comparisons.
func parseAntigravityRevisionConstraint(expression string) ([]antigravityRevisionTerm, bool) {
	var terms []antigravityRevisionTerm
	for _, raw := range strings.Fields(expression) {
		if raw == "" {
			continue
		}
		var op string
		switch {
		case strings.HasPrefix(raw, ">="), strings.HasPrefix(raw, "<="):
			op = raw[:2]
		case strings.HasPrefix(raw, ">"), strings.HasPrefix(raw, "<"), strings.HasPrefix(raw, "="):
			op = raw[:1]
		default:
			return nil, false
		}
		operand := raw[len(op):]
		if strings.Contains(operand, "-") {
			return nil, false
		}
		if _, ok := parseAntigravityRevision(operand); !ok {
			return nil, false
		}
		terms = append(terms, antigravityRevisionTerm{Op: op, Revision: operand})
	}
	return terms, len(terms) > 0
}

// antigravityRevisionSatisfies reports whether rev satisfies every term.
func antigravityRevisionSatisfies(rev antigravityRevision, terms []antigravityRevisionTerm) bool {
	for _, term := range terms {
		bound, ok := parseAntigravityRevision(term.Revision)
		if !ok {
			return false
		}
		cmp := compareAntigravityRevision(rev, bound)
		switch term.Op {
		case ">=":
			if cmp < 0 {
				return false
			}
		case ">":
			if cmp <= 0 {
				return false
			}
		case "<=":
			if cmp > 0 {
				return false
			}
		case "<":
			if cmp >= 0 {
				return false
			}
		case "=":
			if cmp != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// antigravityGlobMatch is globMatch: an anchored `*` wildcard match. Both
// sides are pre-lowercased.
func antigravityGlobMatch(pattern, value string) bool {
	segments := strings.Split(pattern, "*")
	if len(segments) == 1 {
		return value == pattern
	}
	head := segments[0]
	if !strings.HasPrefix(value, head) {
		return false
	}
	remainder := value[len(head):]
	for _, segment := range segments[1 : len(segments)-1] {
		if segment == "" {
			continue
		}
		found := strings.Index(remainder, segment)
		if found < 0 {
			return false
		}
		remainder = remainder[found+len(segment):]
	}
	last := segments[len(segments)-1]
	return last == "" || strings.HasSuffix(remainder, last)
}

// antigravityNonWildcardBytes is nonWildcardBytes: the glob specificity rank.
func antigravityNonWildcardBytes(glob string) int {
	count := 0
	for i := 0; i < len(glob); i++ {
		if glob[i] != '*' {
			count++
		}
	}
	return count
}

// antigravityBareOf is bareOf: the id after the last `/`.
func antigravityBareOf(id string) string {
	if slash := strings.LastIndexByte(id, '/'); slash >= 0 {
		return id[slash+1:]
	}
	return id
}

// antigravityBoundedMatch is boundedMatch: token equality or a token followed
// by a separator or digit.
func antigravityBoundedMatch(value, token string) bool {
	if value == token {
		return true
	}
	if !strings.HasPrefix(value, token) || len(token) >= len(value) {
		return false
	}
	next := value[len(token)]
	return next == '-' || next == '_' || next == '.' || next == ':' || (next >= '0' && next <= '9')
}

// antigravityMatcherMatches is matcherMatches.
func antigravityMatcherMatches(matcher antigravityMatcher, lower, bare string) bool {
	switch matcher.Kind {
	case "exact":
		return bare == matcher.Token
	case "bounded":
		return antigravityBoundedMatch(bare, matcher.Token)
	case "namespace":
		var parts []string
		if matcher.Bounded {
			parts = strings.FieldsFunc(lower, func(r rune) bool { return r == '/' || r == '.' || r == ':' })
		} else {
			parts = strings.Split(lower, "/")
		}
		for _, part := range parts {
			if part == "" {
				continue
			}
			if matcher.Bounded {
				if antigravityBoundedMatch(part, matcher.Token) {
					return true
				}
			} else if part == matcher.Token {
				return true
			}
		}
		return false
	case "prefix":
		return strings.HasPrefix(bare, matcher.Token)
	case "glob":
		return antigravityGlobMatch(matcher.Token, bare)
	}
	return false
}

var antigravityMatcherRanks = map[string]int{"exact": 4, "bounded": 3, "namespace": 2, "prefix": 1, "glob": 0}

type antigravityFamilyRank struct {
	id          string
	priority    int
	specificity int
	hasWinner   bool
	firstTied   string
	secondTied  string
	hasTie      bool
}

// antigravityRankFamilies is rankFamilies: the highest-priority, then most
// specific glob that matches, and the pair that tied for it.
func antigravityRankFamilies(class *antigravityClass, subject string) antigravityFamilyRank {
	var ranking antigravityFamilyRank
	for _, family := range class.Families {
		if !antigravityGlobMatch(family.Glob, subject) {
			continue
		}
		specificity := antigravityNonWildcardBytes(family.Glob)
		switch {
		case ranking.hasWinner && ranking.priority == family.Priority && ranking.specificity == specificity && ranking.id != family.ID:
			ranking.firstTied, ranking.secondTied, ranking.hasTie = ranking.id, family.ID, true
		case !ranking.hasWinner || ranking.priority < family.Priority || (ranking.priority == family.Priority && ranking.specificity < specificity):
			ranking = antigravityFamilyRank{id: family.ID, priority: family.Priority, specificity: specificity, hasWinner: true}
		}
	}
	return ranking
}

// antigravityStripClassNamespace is stripClassNamespace: a `vendor.model` id
// whose vendor names one of the class's matchers.
func antigravityStripClassNamespace(class *antigravityClass, bare string) (string, bool) {
	separator := strings.IndexAny(bare, ".:")
	if separator <= 0 || separator == len(bare)-1 {
		return "", false
	}
	namespace := bare[:separator]
	for _, matcher := range class.Matchers {
		if matcher.Token == namespace {
			return bare[separator+1:], true
		}
	}
	return "", false
}

// antigravityClassifyFamily is classifyFamily; a tie resolves to no family
// (discovery normalizes leniently, which is the only caller).
func antigravityClassifyFamily(class *antigravityClass, bare, model string) string {
	initial := antigravityRankFamilies(class, bare)
	if !initial.hasTie {
		if !initial.hasWinner {
			return ""
		}
		return initial.id
	}
	if scoped, ok := antigravityStripClassNamespace(class, bare); ok {
		rescored := antigravityRankFamilies(class, scoped)
		if rescored.hasWinner && !rescored.hasTie {
			return rescored.id
		}
	}
	return ""
}

// antigravityExtractRevision is extractRevision.
func antigravityExtractRevision(class *antigravityClass, bare string) string {
	for _, skip := range class.SkipBare {
		if skip == bare {
			return ""
		}
	}
	for _, rule := range class.RevisionPrefixes {
		var tail string
		switch {
		case rule.Anywhere:
			if start := strings.Index(bare, rule.Prefix); start >= 0 {
				tail = bare[start+len(rule.Prefix):]
			}
		case strings.HasPrefix(bare, rule.Prefix):
			tail = bare[len(rule.Prefix):]
		}
		if tail == "" {
			continue
		}
		digit := strings.IndexAny(tail, "0123456789")
		if digit < 0 {
			return ""
		}
		revision, ok := parseAntigravityRevisionPrefix(tail[digit:])
		if !ok {
			return ""
		}
		return formatAntigravityRevision(revision)
	}
	return ""
}

// antigravityRanks is ClassRanks.
type antigravityRanks struct {
	class    string
	family   string
	revision string
}

// antigravityClassifyRanks is classifyRanks; an ambiguous class resolves to
// unknown (lenient classification, as discovery normalizes).
func antigravityClassifyRanks(model string) antigravityRanks {
	lower := strings.TrimSpace(asciiLower(model))
	bare := antigravityBareOf(lower)
	var winner *antigravityClass
	winnerRank := 0
	winnerBytes := 0
	tied := false
	for i := range antigravityRules.Taxonomy.Classes {
		class := &antigravityRules.Taxonomy.Classes[i]
		for _, matcher := range class.Matchers {
			if !antigravityMatcherMatches(matcher, lower, bare) {
				continue
			}
			rank := antigravityMatcherRanks[matcher.Kind]
			if winner != nil {
				if winnerRank == rank && winnerBytes == len(matcher.Token) && winner.ID != class.ID {
					tied = true
					continue
				}
				if !(winnerRank < rank || (winnerRank == rank && winnerBytes < len(matcher.Token))) {
					continue
				}
			}
			winner, winnerRank, winnerBytes, tied = class, rank, len(matcher.Token), false
		}
	}
	// Two classes tie for the id: discovery classifies leniently, so the
	// identity is unknown rather than an error.
	if winner == nil || tied {
		return antigravityRanks{class: "unknown"}
	}
	ranks := antigravityRanks{class: winner.ID, family: antigravityClassifyFamily(winner, bare, lower)}
	ranks.revision = antigravityExtractRevision(winner, bare)
	return ranks
}

// antigravityRanksInClass is ranksInClass.
func antigravityRanksInClass(classID, model string) antigravityRanks {
	for i := range antigravityRules.Taxonomy.Classes {
		class := &antigravityRules.Taxonomy.Classes[i]
		if class.ID != classID {
			continue
		}
		lower := strings.TrimSpace(asciiLower(model))
		bare := antigravityBareOf(lower)
		return antigravityRanks{class: classID, family: antigravityClassifyFamily(class, bare, lower), revision: antigravityExtractRevision(class, bare)}
	}
	return antigravityRanks{class: classID}
}

// antigravityIdentityOverride is findIdentityOverride. Discovery classifies
// leniently, so an ambiguous pattern override resolves to none.
func antigravityIdentityOverride(provider, bareModel string) *antigravityIdentityEntry {
	lowerProvider := asciiLower(provider)
	lowerBare := asciiLower(bareModel)
	// Exact overrides: a provider-scoped entry wins over a provider-agnostic
	// one stating the same id.
	var scopedExact, agnosticExact *antigravityIdentityEntry
	for i := range antigravityRules.Taxonomy.Classes {
		class := &antigravityRules.Taxonomy.Classes[i]
		for j := range class.Overrides {
			entry := &class.Overrides[j]
			if entry.Model == "" || entry.Model != lowerBare {
				continue
			}
			if entry.Provider == lowerProvider {
				if scopedExact == nil {
					scopedExact = entry
				}
			} else if entry.Provider == "" && agnosticExact == nil {
				agnosticExact = entry
			}
		}
	}
	if scopedExact != nil {
		return scopedExact
	}
	if agnosticExact != nil {
		return agnosticExact
	}
	// Pattern overrides: provider-scoped patterns rank by glob specificity and
	// beat provider-agnostic ones entirely; an equal-specificity tie resolves
	// to none.
	var bestScoped, bestAgnostic *antigravityIdentityEntry
	bestScopedBytes, bestAgnosticBytes := -1, -1
	tieScoped, tieAgnostic := false, false
	for i := range antigravityRules.Taxonomy.Classes {
		class := &antigravityRules.Taxonomy.Classes[i]
		for j := range class.Overrides {
			entry := &class.Overrides[j]
			if entry.Glob == "" || !antigravityGlobMatch(entry.Glob, lowerBare) {
				continue
			}
			specificity := antigravityNonWildcardBytes(entry.Glob)
			switch entry.Provider {
			case lowerProvider:
				switch {
				case bestScoped == nil || bestScopedBytes < specificity:
					bestScoped, bestScopedBytes, tieScoped = entry, specificity, false
				case bestScopedBytes == specificity:
					tieScoped = true
				}
			case "":
				switch {
				case bestAgnostic == nil || bestAgnosticBytes < specificity:
					bestAgnostic, bestAgnosticBytes, tieAgnostic = entry, specificity, false
				case bestAgnosticBytes == specificity:
					tieAgnostic = true
				}
			}
		}
	}
	if bestScoped != nil {
		if tieScoped {
			return nil
		}
		return bestScoped
	}
	if bestAgnostic != nil && !tieAgnostic {
		return bestAgnostic
	}
	return nil
}

// antigravityCollapsedVariant is CollapsedVariant.
type antigravityCollapsedVariant struct {
	LogicalID       string
	Effort          string
	ThinkingVariant bool
}

// antigravityCollapseVariantID is collapseVariantId: an exact effort-family
// alias, else the longest declared suffix.
func antigravityCollapseVariantID(provider, model string) antigravityCollapsedVariant {
	lowerProvider := asciiLower(provider)
	lower := asciiLower(model)
	for _, family := range antigravityRules.Taxonomy.EffortFamilies {
		if family.Provider != lowerProvider {
			continue
		}
		for _, alias := range family.Aliases {
			if alias == lower {
				return antigravityCollapsedVariant{LogicalID: family.Logical}
			}
		}
	}
	bare := antigravityBareOf(lower)
	var winner *antigravitySuffix
	for i := range antigravityRules.Taxonomy.Suffixes {
		rule := &antigravityRules.Taxonomy.Suffixes[i]
		if !strings.HasSuffix(lower, rule.Suffix) {
			continue
		}
		if rule.ExceptBarePrefix != "" && strings.HasPrefix(bare, rule.ExceptBarePrefix) {
			continue
		}
		if winner == nil || len(rule.Suffix) > len(winner.Suffix) {
			winner = rule
		}
	}
	if winner != nil {
		return antigravityCollapsedVariant{
			LogicalID:       model[:len(model)-len(winner.Suffix)],
			Effort:          winner.Effort,
			ThinkingVariant: winner.Thinking,
		}
	}
	for _, lane := range antigravityRules.Taxonomy.Lanes {
		if !slicesContains(lane.Providers, lowerProvider) || !strings.HasSuffix(lower, lane.Suffix) {
			continue
		}
		trimmed := lower[:len(lower)-len(lane.Suffix)]
		trimmedBare := antigravityBareOf(trimmed)
		if lane.BarePrefix != "" && !strings.HasPrefix(trimmedBare, lane.BarePrefix) {
			continue
		}
		var effortRule *antigravitySuffix
		for i := range antigravityRules.Taxonomy.Suffixes {
			rule := &antigravityRules.Taxonomy.Suffixes[i]
			if rule.Effort == "" || !strings.HasSuffix(trimmed, rule.Suffix) {
				continue
			}
			if rule.ExceptBarePrefix != "" && strings.HasPrefix(trimmedBare, rule.ExceptBarePrefix) {
				continue
			}
			if effortRule == nil || len(rule.Suffix) > len(effortRule.Suffix) {
				effortRule = rule
			}
		}
		if effortRule == nil {
			continue
		}
		base := model[:len(trimmed)-len(effortRule.Suffix)]
		if base == "" || strings.HasSuffix(base, "/") {
			continue
		}
		return antigravityCollapsedVariant{LogicalID: base + model[len(trimmed):], Effort: effortRule.Effort}
	}
	return antigravityCollapsedVariant{LogicalID: model}
}

func slicesContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// antigravityClassifyModel is classifyModel: a reviewed override, then the
// suffix collapse, then class/family/revision ranks.
func antigravityClassifyModel(provider, modelID string) antigravityIdentity {
	trimmed := strings.TrimSpace(modelID)
	bare := antigravityBareOf(trimmed)
	if entry := antigravityIdentityOverride(provider, bare); entry != nil {
		logical := trimmed
		if entry.Logical != "" {
			logical = entry.Logical
		}
		class := entry.Class
		if class == "" {
			class = antigravityClassifyRanks(logical).class
		}
		inferred := antigravityRanksInClass(class, logical)
		identity := antigravityIdentity{Class: class}
		if entry.Family != "" {
			identity.Family = entry.Family
		} else {
			identity.Family = inferred.family
		}
		if entry.Revision != "" {
			identity.Revision = entry.Revision
		} else {
			identity.Revision = inferred.revision
		}
		identity.Effort = entry.Effort
		identity.ThinkingVariant = entry.ThinkingVariant
		if logical != trimmed {
			identity.LogicalID = logical
		}
		return identity
	}
	collapsed := antigravityCollapsedVariant{LogicalID: trimmed}
	if trimmed == modelID {
		collapsed = antigravityCollapseVariantID(provider, trimmed)
	}
	ranks := antigravityClassifyRanks(collapsed.LogicalID)
	identity := antigravityIdentity{
		Class:           ranks.class,
		Family:          ranks.family,
		Revision:        ranks.revision,
		Effort:          collapsed.Effort,
		ThinkingVariant: collapsed.ThinkingVariant,
	}
	if collapsed.LogicalID != trimmed {
		identity.LogicalID = collapsed.LogicalID
	}
	return identity
}

// antigravityStripThinkingSuffix is stripThinkingVariantSuffix: removes the
// first declared pair token used as a thinking-variant marker.
func antigravityStripThinkingSuffix(model string) (string, bool) {
	lower := asciiLower(model)
	for _, token := range antigravityRules.Taxonomy.PairTokens {
		needle := "-" + token
		searchFrom := 0
		for searchFrom < len(lower) {
			index := strings.Index(lower[searchFrom:], needle)
			if index < 0 {
				break
			}
			index += searchFrom
			end := index + len(needle)
			followedByToken := false
			if end < len(lower) {
				next := lower[end]
				followedByToken = (next >= '0' && next <= '9') || (next >= 'a' && next <= 'z')
			}
			wordStart := index
			for wordStart > 0 {
				code := lower[wordStart-1]
				if !((code >= '0' && code <= '9') || (code >= 'a' && code <= 'z')) {
					break
				}
				wordStart--
			}
			preceding := lower[wordStart:index]
			if !followedByToken && preceding != "non" && preceding != "no" {
				stripped := model[:index] + model[end:]
				if stripped == "" {
					return "", false
				}
				return stripped, true
			}
			searchFrom = index + 1
		}
	}
	return "", false
}
