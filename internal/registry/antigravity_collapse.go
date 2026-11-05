package registry

import (
	"regexp"
	"strings"
)

// This file ports NeoPi's compat/collapse.ts for google-antigravity: the
// reviewed variant table (effort-tier families, revision templates, retired
// members, aliases) and the automatic X / X-thinking pair rule, as
// collapseVariants applies them to one provider's roster.

const antigravityRevisionPlaceholder = "{rev}"

// antigravityRevisionCapture is REVISION_CAPTURE.
const antigravityRevisionCapture = `(\d+(?:\.\d+){0,2})`

// antigravityRoutingKeys is VARIANT_ROUTING_KEYS: "off" then THINKING_EFFORTS.
var antigravityRoutingKeys = append([]string{"off"}, neoPiThinkingEfforts...)

// antigravityFamily is EffortVariantFamily: one collapsed family's logical id,
// member wire ids and per-effort routing.
type antigravityFamily struct {
	ID                         string
	Name                       string
	Members                    []string
	DefaultMember              string
	RetiredMembers             []string
	Routing                    map[string]string
	Thinking                   *NeoPiThinking
	SuppressWhenOff            bool
	PreserveAbsentEffortRoutes bool
	ExtraAliases               []string
}

// antigravityTemplate is VariantFamilyTemplate: a family whose ids carry
// `{rev}`, instantiated for every revision the roster advertises.
type antigravityTemplate struct {
	Family   antigravityFamily
	Revision []antigravityRevisionTerm
	Pattern  *regexp.Regexp
}

var antigravityCollapseTable = func() struct {
	Families  []antigravityFamily
	Templates []antigravityTemplate
	Aliases   map[string]string
} {
	var table struct {
		Families  []antigravityFamily
		Templates []antigravityTemplate
		Aliases   map[string]string
	}
	for _, compiled := range antigravityRules.VariantFamilies {
		if strings.Contains(compiled.ID, antigravityRevisionPlaceholder) {
			table.Templates = append(table.Templates, antigravityCompiledTemplate(compiled))
			continue
		}
		table.Families = append(table.Families, antigravityCompiledFamily(compiled))
	}
	table.Aliases = antigravityRules.ProviderAliases
	return table
}()

// antigravityCompiledFamily is compiledFamily.
func antigravityCompiledFamily(compiled antigravityVariantFamily) antigravityFamily {
	family := antigravityFamily{
		ID:                         compiled.ID,
		Name:                       compiled.Name,
		Members:                    compiled.Members,
		DefaultMember:              compiled.DefaultMember,
		RetiredMembers:             compiled.RetiredMembers,
		Routing:                    compiled.Routing,
		SuppressWhenOff:            compiled.SuppressWhenOff,
		PreserveAbsentEffortRoutes: compiled.PreserveAbsentEffortRoutes,
		ExtraAliases:               compiled.ExtraAliases,
	}
	if !compiled.NoThinking && compiled.Mode != "" {
		family.Thinking = &NeoPiThinking{
			Mode:           compiled.Mode,
			Efforts:        compiled.Efforts,
			EffortBudgets:  compiled.EffortBudgets,
			DefaultLevel:   compiled.DefaultLevel,
			RequiresEffort: compiled.RequiresEffort,
		}
	}
	return family
}

// antigravityCompiledTemplate is compiledTemplate: the family with its id
// matcher, one capture group per `{rev}` alternative.
func antigravityCompiledTemplate(compiled antigravityVariantFamily) antigravityTemplate {
	family := antigravityCompiledFamily(compiled)
	alternatives := make([]string, 0, 1+len(family.Members)+len(family.ExtraAliases))
	for _, id := range append(append([]string{family.ID}, family.Members...), family.ExtraAliases...) {
		parts := strings.Split(id, antigravityRevisionPlaceholder)
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		alternatives = append(alternatives, strings.Join(parts, antigravityRevisionCapture))
	}
	template := antigravityTemplate{
		Family:  family,
		Pattern: regexp.MustCompile(`(?i)^(?:` + strings.Join(alternatives, "|") + `)$`),
	}
	if terms, ok := parseAntigravityRevisionConstraint(compiled.Revision); ok {
		template.Revision = terms
	}
	return template
}

// antigravityInstantiateTemplate is instantiateTemplate: the family for one
// revision, when that revision is in range.
func antigravityInstantiateTemplate(template antigravityTemplate, rev string) (antigravityFamily, bool) {
	if template.Revision != nil {
		parsed, ok := parseAntigravityRevision(rev)
		if !ok || !antigravityRevisionSatisfies(parsed, template.Revision) {
			return antigravityFamily{}, false
		}
	}
	fill := func(id string) string { return strings.ReplaceAll(id, antigravityRevisionPlaceholder, rev) }
	source := template.Family
	family := antigravityFamily{
		ID:                         fill(source.ID),
		Name:                       fill(source.Name),
		DefaultMember:              fill(source.DefaultMember),
		Thinking:                   source.Thinking,
		SuppressWhenOff:            source.SuppressWhenOff,
		PreserveAbsentEffortRoutes: source.PreserveAbsentEffortRoutes,
	}
	for _, member := range source.Members {
		family.Members = append(family.Members, fill(member))
	}
	for _, retired := range source.RetiredMembers {
		family.RetiredMembers = append(family.RetiredMembers, fill(retired))
	}
	for _, alias := range source.ExtraAliases {
		family.ExtraAliases = append(family.ExtraAliases, fill(alias))
	}
	if source.Routing != nil {
		family.Routing = make(map[string]string, len(source.Routing))
		for effort, target := range source.Routing {
			family.Routing[effort] = fill(target)
		}
	}
	return family, true
}

// antigravityTemplateFamilyFor is templateFamilyFor: the family a template
// yields for id (logical id, member or alias).
func antigravityTemplateFamilyFor(templates []antigravityTemplate, id string) (antigravityFamily, bool) {
	for _, template := range templates {
		match := template.Pattern.FindStringSubmatch(id)
		if match == nil {
			continue
		}
		rev := ""
		for _, group := range match[1:] {
			if group != "" {
				rev = group
				break
			}
		}
		if rev == "" {
			continue
		}
		if family, ok := antigravityInstantiateTemplate(template, rev); ok {
			return family, true
		}
	}
	return antigravityFamily{}, false
}

// antigravityInstantiateTemplates is instantiateTemplates: one family per
// revision the roster advertises, skipping ids a concrete family owns.
func antigravityInstantiateTemplates(ids []string) []antigravityFamily {
	templates := antigravityCollapseTable.Templates
	if len(templates) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(antigravityCollapseTable.Families))
	for _, family := range antigravityCollapseTable.Families {
		seen[family.ID] = true
	}
	var out []antigravityFamily
	for _, id := range ids {
		family, ok := antigravityTemplateFamilyFor(templates, id)
		if !ok || seen[family.ID] {
			continue
		}
		seen[family.ID] = true
		out = append(out, family)
	}
	return out
}

// antigravityAliasIndex is VariantAliasIndex: member and alias ids → their
// family's logical id, with templated families materialized on first lookup.
type antigravityAliasIndex struct {
	forward  map[string]string
	families map[string]bool
}

func newAntigravityAliasIndex() *antigravityAliasIndex {
	index := &antigravityAliasIndex{forward: map[string]string{}, families: map[string]bool{}}
	for _, family := range antigravityCollapseTable.Families {
		index.addFamily(family)
	}
	return index
}

func (index *antigravityAliasIndex) add(from, to string) bool {
	lower := asciiLower(from)
	if from == to || index.forward[lower] != "" {
		return false
	}
	index.forward[lower] = to
	return true
}

func (index *antigravityAliasIndex) addFamily(family antigravityFamily) {
	index.families[family.ID] = true
	for _, member := range family.Members {
		index.add(member, family.ID)
	}
	for _, alias := range family.ExtraAliases {
		index.add(alias, family.ID)
	}
}

func (index *antigravityAliasIndex) learn(id string) {
	if len(antigravityCollapseTable.Templates) == 0 {
		return
	}
	family, ok := antigravityTemplateFamilyFor(antigravityCollapseTable.Templates, id)
	if !ok || index.families[family.ID] {
		return
	}
	index.addFamily(family)
}

// resolve is resolve: the logical id an id is a member or alias of.
func (index *antigravityAliasIndex) resolve(id string) (string, bool) {
	index.learn(id)
	replacement, ok := index.forward[asciiLower(id)]
	return replacement, ok
}

// isFamily is isFamily: whether id is a collapsed logical id.
func (index *antigravityAliasIndex) isFamily(id string) bool {
	index.learn(id)
	return index.families[id]
}

var antigravityReviewedAliases = newAntigravityAliasIndex()

// AntigravityIsReviewedTierWireID is isReviewedTierVariantWireId: whether
// wireModelID is one tier of a reviewed family whose efforts come from
// distinct sibling SKUs, so the SKU (not the effort) fixes the thinking
// strength.
func AntigravityIsReviewedTierWireID(wireModelID string) bool {
	id := strings.TrimSpace(wireModelID)
	var family antigravityFamily
	found := false
	for _, candidate := range antigravityCollapseTable.Families {
		if slicesContains(candidate.Members, id) {
			family, found = candidate, true
			break
		}
	}
	if !found {
		family, found = antigravityTemplateFamilyFor(antigravityCollapseTable.Templates, id)
	}
	if !found {
		return false
	}
	tiers := map[string]bool{}
	for _, key := range antigravityRoutingKeys {
		if key == "off" {
			continue
		}
		if target, ok := family.Routing[key]; ok {
			tiers[target] = true
		}
	}
	return len(tiers) > 1 && tiers[id]
}

// antigravityIsCollapsed is isCollapsedVariantSpec scoped to this provider:
// `effortRouting` is written only by collapsing, and a `requestModelId` only
// lands on a row a reviewed family owns.
func antigravityIsCollapsed(spec antigravityModel) bool {
	if spec.Thinking != nil && spec.Thinking.EffortRouting != nil {
		return true
	}
	if !spec.HasRequestModelID {
		return false
	}
	return antigravityReviewedAliases.isFamily(spec.ID)
}

// antigravityRetiredSet is the family's retired members, when it has any.
func antigravityRetiredSet(family antigravityFamily) map[string]bool {
	if len(family.RetiredMembers) == 0 {
		return nil
	}
	retired := make(map[string]bool, len(family.RetiredMembers))
	for _, id := range family.RetiredMembers {
		retired[id] = true
	}
	return retired
}

// antigravityReconcileRetiredRouting is reconcileRetiredRouting: re-point a
// stale collapsed row's default wire id and routing away from retired ids.
func antigravityReconcileRetiredRouting(spec antigravityModel, family antigravityFamily, retired map[string]bool) (antigravityModel, bool) {
	if retired == nil {
		return spec, false
	}
	routing := map[string]string(nil)
	if spec.Thinking != nil {
		routing = spec.Thinking.EffortRouting
	}
	requestRetired := spec.HasRequestModelID && retired[spec.RequestModelID]
	routingRetired := false
	for _, key := range antigravityRoutingKeys {
		if target, ok := routing[key]; ok && retired[target] {
			routingRetired = true
			break
		}
	}
	if !requestRetired && !routingRetired {
		return spec, false
	}
	offTarget := family.Routing["off"]
	fallbackWireID := ""
	if offTarget != "" && !retired[offTarget] {
		fallbackWireID = offTarget
	} else {
		for _, id := range family.Members {
			if !retired[id] {
				fallbackWireID = id
				break
			}
		}
	}
	if routingRetired && routing != nil && spec.Thinking != nil {
		next := make(map[string]string, len(routing))
		for _, key := range antigravityRoutingKeys {
			target, ok := routing[key]
			if !ok {
				continue
			}
			if !retired[target] {
				next[key] = target
				continue
			}
			if tableTarget := family.Routing[key]; tableTarget != "" && !retired[tableTarget] {
				next[key] = tableTarget
			} else if fallbackWireID != "" {
				next[key] = fallbackWireID
			}
		}
		thinking := *spec.Thinking
		thinking.EffortRouting = next
		spec.Thinking = &thinking
	}
	if requestRetired {
		if fallbackWireID != "" && fallbackWireID != spec.ID {
			spec.RequestModelID, spec.HasRequestModelID = fallbackWireID, true
		} else {
			spec.RequestModelID, spec.HasRequestModelID = "", false
		}
	}
	return spec, true
}

// antigravityRefreshCollapsedThinking is refreshCollapsedThinking: re-apply a
// budget-family's transport, budgets and routing to a stale snapshot.
func antigravityRefreshCollapsedThinking(spec antigravityModel, family antigravityFamily, retired map[string]bool) (antigravityModel, bool) {
	familyThinking := family.Thinking
	if !spec.Reasoning || familyThinking == nil || familyThinking.EffortBudgets == nil {
		return spec, false
	}
	routing := map[string]string{}
	hasRouting := false
	for _, key := range antigravityRoutingKeys {
		target, ok := family.Routing[key]
		if !ok || retired[target] {
			continue
		}
		routing[key] = target
		hasRouting = true
	}
	thinking := *familyThinking
	thinking.Efforts = append([]string(nil), familyThinking.Efforts...)
	thinking.EffortBudgets = familyThinking.EffortBudgets
	if hasRouting {
		thinking.EffortRouting = routing
	}
	thinking.SuppressWhenOff = family.SuppressWhenOff
	offTarget := family.Routing["off"]
	requestModelID, hasRequestModelID := spec.RequestModelID, spec.HasRequestModelID
	if offTarget != "" && !retired[offTarget] && offTarget != spec.ID {
		requestModelID, hasRequestModelID = offTarget, true
	}
	if antigravityThinkingEqual(&thinking, spec.Thinking) && requestModelID == spec.RequestModelID && hasRequestModelID == spec.HasRequestModelID {
		return spec, false
	}
	spec.Thinking = &thinking
	spec.RequestModelID, spec.HasRequestModelID = requestModelID, hasRequestModelID
	return spec, true
}

// antigravityReconcileDefaultMember is reconcileDefaultMember: re-point a
// stale default wire id at the family's declared default member.
func antigravityReconcileDefaultMember(spec antigravityModel, family antigravityFamily, present map[string]bool) (antigravityModel, bool) {
	defaultMember := family.DefaultMember
	if defaultMember == "" || defaultMember == spec.ID {
		return spec, false
	}
	target := ""
	if present == nil || present[defaultMember] {
		target = defaultMember
	} else {
		for _, id := range family.Members {
			if present[id] {
				target = id
				break
			}
		}
	}
	if target == "" || (spec.HasRequestModelID && spec.RequestModelID == target) {
		return spec, false
	}
	if spec.Thinking == nil || spec.Thinking.EffortRouting == nil {
		return spec, false
	}
	for _, key := range antigravityRoutingKeys {
		if spec.Thinking.EffortRouting[key] == target {
			spec.RequestModelID, spec.HasRequestModelID = target, true
			return spec, true
		}
	}
	return spec, false
}

// antigravityWireProfilesOf is antigravityWireProfilesOf: the union of every
// member's discovered wire profile, keyed by the member's own wire id.
func antigravityWireProfilesOf(members []antigravityModel) map[string]NeoPiAntigravityWireProfile {
	var profiles map[string]NeoPiAntigravityWireProfile
	for _, member := range members {
		if len(member.WireProfiles) == 0 {
			continue
		}
		if profiles == nil {
			profiles = map[string]NeoPiAntigravityWireProfile{}
		}
		for wireID, profile := range member.WireProfiles {
			profiles[wireID] = profile
		}
	}
	return profiles
}

// antigravityThinkingEqual is Bun.deepEquals over the thinking surface.
func antigravityThinkingEqual(a, b *NeoPiThinking) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Mode != b.Mode || a.RequiresEffort != b.RequiresEffort || a.SuppressWhenOff != b.SuppressWhenOff ||
		a.DefaultLevel != b.DefaultLevel || a.Unverified != b.Unverified || len(a.Efforts) != len(b.Efforts) ||
		len(a.EffortRouting) != len(b.EffortRouting) || len(a.EffortMap) != len(b.EffortMap) ||
		len(a.EffortBudgets) != len(b.EffortBudgets) {
		return false
	}
	for i := range a.Efforts {
		if a.Efforts[i] != b.Efforts[i] {
			return false
		}
	}
	for key, value := range a.EffortRouting {
		if b.EffortRouting[key] != value {
			return false
		}
	}
	for key, value := range a.EffortMap {
		if b.EffortMap[key] != value {
			return false
		}
	}
	for key, value := range a.EffortBudgets {
		if b.EffortBudgets[key] != value {
			return false
		}
	}
	return true
}

// antigravityMaxOrNull is maxOrNull: the largest known value, 0 when none.
func antigravityMaxOrNull(values []int) int {
	max := 0
	known := false
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if !known || value > max {
			max, known = value, true
		}
	}
	return max
}

// antigravityCollapseWithTable is collapseWithTable: replace every family's
// members with one logical row, healing already-collapsed snapshots in place.
func antigravityCollapseWithTable(specs []antigravityModel, families []antigravityFamily) []antigravityModel {
	byID := make(map[string]int, len(specs))
	for i, spec := range specs {
		if _, ok := byID[spec.ID]; !ok {
			byID[spec.ID] = i
		}
	}
	replacement := map[string]antigravityModel{}
	familyIDBySpecID := map[string]string{}
	families = append(append([]antigravityFamily(nil), families...), antigravityInstantiateTemplates(orderedIDs(specs))...)
	for _, family := range families {
		retired := antigravityRetiredSet(family)
		existingIndex, hasExisting := byID[family.ID]
		var existing antigravityModel
		existingCollapsed := false
		if hasExisting {
			existing = specs[existingIndex]
			existingCollapsed = antigravityIsCollapsed(existing)
		}
		reconciled, reconciledChanged := existing, false
		if hasExisting && existingCollapsed && retired != nil {
			reconciled, reconciledChanged = antigravityReconcileRetiredRouting(existing, family, retired)
		}
		rawPresent := make([]string, 0, len(family.Members))
		for _, id := range family.Members {
			if _, ok := byID[id]; ok && !(id == family.ID && existingCollapsed) {
				rawPresent = append(rawPresent, id)
			}
		}
		if len(rawPresent) == 0 {
			// Inert (no members) or already collapsed (pass-through). A stale
			// snapshot keyed on the family id is healed in place from the current
			// family: retired targets drop, the transport and budgets refresh.
			if hasExisting && existingCollapsed {
				refreshed, changed := reconciled, reconciledChanged
				if next, refreshedThinking := antigravityRefreshCollapsedThinking(refreshed, family, retired); refreshedThinking {
					refreshed, changed = next, true
				}
				if next, rePointed := antigravityReconcileDefaultMember(refreshed, family, nil); rePointed {
					refreshed, changed = next, true
				}
				if changed {
					familyIDBySpecID[family.ID] = family.ID
					replacement[family.ID] = refreshed
				}
			}
			continue
		}
		for _, id := range rawPresent {
			familyIDBySpecID[id] = family.ID
		}
		if hasExisting {
			familyIDBySpecID[family.ID] = family.ID
		}
		memberSpecs := make([]antigravityModel, 0, len(rawPresent))
		for _, id := range rawPresent {
			memberSpecs = append(memberSpecs, specs[byID[id]])
		}
		if existingCollapsed {
			// Mixed input: the collapsed entry wins; stale raw members are
			// deduped away.
			present := make(map[string]bool, len(rawPresent))
			for _, id := range rawPresent {
				present[id] = true
			}
			merged := reconciled
			if next, changed := antigravityReconcileDefaultMember(merged, family, present); changed {
				merged = next
			}
			if profiles := antigravityWireProfilesOf(memberSpecs); profiles != nil {
				merged.WireProfiles = antigravityMergeWireProfiles(merged.WireProfiles, profiles)
			}
			replacement[family.ID] = merged
			continue
		}
		first := memberSpecs[0]
		present := make(map[string]bool, len(rawPresent))
		for _, id := range rawPresent {
			present[id] = true
		}
		routing := map[string]string{}
		hasRouting := false
		hasEffortRoute := false
		usedAbsentEffortRoute := false
		for _, effort := range antigravityRoutingKeys {
			target, ok := family.Routing[effort]
			if !ok {
				continue
			}
			targetPresent := present[target]
			preserveAbsent := effort != "off" && family.PreserveAbsentEffortRoutes
			if (targetPresent || preserveAbsent) && !retired[target] {
				routing[effort] = target
				hasRouting = true
				if effort != "off" {
					hasEffortRoute = true
				}
				if !targetPresent && effort != "off" {
					usedAbsentEffortRoute = true
				}
			}
		}
		reasoning := hasEffortRoute
		for _, member := range memberSpecs {
			if member.Reasoning {
				reasoning = true
				break
			}
		}
		var thinking *NeoPiThinking
		if family.Thinking != nil {
			copied := *family.Thinking
			copied.Efforts = append([]string(nil), family.Thinking.Efforts...)
			copied.EffortBudgets = family.Thinking.EffortBudgets
			thinking = &copied
			if hasRouting {
				thinking.EffortRouting = routing
			}
			thinking.SuppressWhenOff = family.SuppressWhenOff
		}
		input := make([]string, 0, 2)
		for _, modality := range []string{"text", "image"} {
			for _, member := range memberSpecs {
				if slicesContains(member.Input, modality) {
					input = append(input, modality)
					break
				}
			}
		}
		contextWindows := make([]int, 0, len(memberSpecs))
		maxTokens := make([]int, 0, len(memberSpecs))
		for _, member := range memberSpecs {
			contextWindows = append(contextWindows, member.ContextWindow)
			maxTokens = append(maxTokens, member.MaxTokens)
		}
		collapsed := first
		collapsed.ID = family.ID
		collapsed.Name = family.Name
		collapsed.Reasoning = reasoning
		collapsed.Input = input
		collapsed.ContextWindow = antigravityMaxOrNull(contextWindows)
		collapsed.MaxTokens = antigravityMaxOrNull(maxTokens)
		if profiles := antigravityWireProfilesOf(memberSpecs); profiles != nil {
			collapsed.WireProfiles = profiles
		}
		defaultWireID := ""
		if family.DefaultMember != "" && present[family.DefaultMember] && !retired[family.DefaultMember] {
			defaultWireID = family.DefaultMember
		}
		if defaultWireID == "" {
			for _, id := range rawPresent {
				if !retired[id] {
					defaultWireID = id
					break
				}
			}
		}
		if defaultWireID == "" {
			defaultWireID = rawPresent[0]
		}
		if defaultWireID == family.ID {
			if usedAbsentEffortRoute {
				collapsed.RequestModelID, collapsed.HasRequestModelID = defaultWireID, true
			} else {
				collapsed.RequestModelID, collapsed.HasRequestModelID = "", false
			}
		} else {
			collapsed.RequestModelID, collapsed.HasRequestModelID = defaultWireID, true
		}
		// A surface-less family (single wire id, uid-encoded effort) keeps
		// `reasoning` but carries no thinking: every tier would resolve to the
		// same upstream id.
		if reasoning && thinking != nil {
			collapsed.Thinking = thinking
		} else {
			collapsed.Thinking = nil
		}
		replacement[family.ID] = collapsed
	}

	// Refresh stale alias-keyed snapshots in place (recycled bare ids).
	for _, family := range antigravityCollapseTable.Families {
		retired := antigravityRetiredSet(family)
		for _, alias := range family.ExtraAliases {
			if alias == family.ID || familyIDBySpecID[alias] != "" {
				continue
			}
			index, ok := byID[alias]
			if !ok {
				continue
			}
			refreshed, changed := antigravityRefreshCollapsedThinking(specs[index], family, retired)
			if changed {
				familyIDBySpecID[alias] = alias
				replacement[alias] = refreshed
			}
		}
	}

	if len(replacement) == 0 {
		return append([]antigravityModel(nil), specs...)
	}
	emitted := map[string]bool{}
	out := make([]antigravityModel, 0, len(specs))
	for _, spec := range specs {
		familyID, ok := familyIDBySpecID[spec.ID]
		if !ok {
			out = append(out, spec)
			continue
		}
		if emitted[familyID] {
			continue
		}
		emitted[familyID] = true
		if familySpec, ok := replacement[familyID]; ok {
			out = append(out, familySpec)
		}
	}
	return out
}

func orderedIDs(specs []antigravityModel) []string {
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	return ids
}

func antigravityMergeWireProfiles(base, overlay map[string]NeoPiAntigravityWireProfile) map[string]NeoPiAntigravityWireProfile {
	merged := make(map[string]NeoPiAntigravityWireProfile, len(base)+len(overlay))
	for wireID, profile := range base {
		merged[wireID] = profile
	}
	for wireID, profile := range overlay {
		merged[wireID] = profile
	}
	return merged
}

// antigravityPairFamily is deriveThinkingPairFamilies' family for one live
// X / X-thinking pair the reviewed table does not claim.
func antigravityPairFamily(thinking antigravityModel, base antigravityModel) (antigravityFamily, error) {
	surface, err := antigravityPairThinkingSurface(thinking, base)
	if err != nil {
		return antigravityFamily{}, err
	}
	routing := map[string]string{"off": base.ID}
	for _, effort := range surface.Efforts {
		routing[effort] = thinking.ID
	}
	return antigravityFamily{
		ID:       base.ID,
		Name:     base.Name,
		Members:  []string{base.ID, thinking.ID},
		Routing:  routing,
		Thinking: surface,
	}, nil
}

// antigravityPairThinkingSurface is derivePairThinkingSurface: the thinking
// member's metadata, else the bare member's, else the canonical deriver.
func antigravityPairThinkingSurface(thinking, base antigravityModel) (*NeoPiThinking, error) {
	baked := thinking.Thinking
	if baked == nil {
		baked = base.Thinking
	}
	if baked != nil && len(baked.Efforts) > 0 {
		surface := *baked
		surface.Efforts = append([]string(nil), baked.Efforts...)
		surface.EffortRouting = nil
		surface.SuppressWhenOff = false
		surface.RequiresEffort = false
		return &surface, nil
	}
	derived, err := antigravityBuildModel(antigravitySpec{
		ID:            thinking.ID,
		Name:          thinking.Name,
		BaseURL:       thinking.BaseURL,
		Reasoning:     true,
		Input:         thinking.Input,
		ContextWindow: thinking.ContextWindow,
		MaxTokens:     thinking.MaxTokens,
		WireProfiles:  thinking.WireProfiles,
	})
	if err != nil {
		return nil, err
	}
	if derived.Thinking != nil && len(derived.Thinking.Efforts) > 0 {
		surface := *derived.Thinking
		surface.Efforts = append([]string(nil), derived.Thinking.Efforts...)
		surface.EffortRouting = nil
		surface.SuppressWhenOff = false
		surface.RequiresEffort = false
		return &surface, nil
	}
	return &NeoPiThinking{Mode: "budget", Efforts: []string{"minimal", "low", "medium", "high"}}, nil
}

// antigravityDerivePairFamilies is deriveThinkingPairFamilies: every live
// X / X-thinking pair the reviewed table does not claim and whose known
// prices agree.
func antigravityDerivePairFamilies(specs []antigravityModel) ([]antigravityFamily, error) {
	byID := make(map[string]int, len(specs))
	for i, spec := range specs {
		if _, ok := byID[spec.ID]; !ok {
			byID[spec.ID] = i
		}
	}
	var families []antigravityFamily
	for _, spec := range specs {
		collapsed := antigravityCollapseVariantID(antigravityProvider, spec.ID)
		baseID := ""
		if collapsed.ThinkingVariant {
			baseID = collapsed.LogicalID
		} else if stripped, ok := antigravityStripThinkingSuffix(spec.ID); ok {
			baseID = stripped
		}
		if baseID == "" || baseID == spec.ID {
			continue
		}
		baseIndex, ok := byID[baseID]
		if !ok {
			continue
		}
		base := specs[baseIndex]
		if _, claimed := antigravityReviewedAliases.resolve(spec.ID); claimed {
			continue
		}
		if _, claimed := antigravityReviewedAliases.resolve(baseID); claimed {
			continue
		}
		if antigravityReviewedAliases.isFamily(spec.ID) || antigravityReviewedAliases.isFamily(baseID) {
			continue
		}
		specPriced := spec.Cost.input != 0 || spec.Cost.output != 0
		basePriced := base.Cost.input != 0 || base.Cost.output != 0
		if specPriced && basePriced && spec.Cost != base.Cost {
			continue
		}
		family, err := antigravityPairFamily(spec, base)
		if err != nil {
			return nil, err
		}
		families = append(families, family)
	}
	return families, nil
}

// antigravityCollapseVariants is collapseVariants for google-antigravity: the
// reviewed table, then the automatic thinking-pair rule.
func antigravityCollapseVariants(specs []antigravityModel) ([]antigravityModel, error) {
	result := antigravityCollapseWithTable(specs, antigravityCollapseTable.Families)
	derived, err := antigravityDerivePairFamilies(result)
	if err != nil {
		return nil, err
	}
	if len(derived) > 0 {
		result = antigravityCollapseWithTable(result, derived)
	}
	return result, nil
}
