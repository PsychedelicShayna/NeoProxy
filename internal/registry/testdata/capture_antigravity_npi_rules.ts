// Exports the slice of NeoPi's compiled rule tree (compat/rules.json) that
// resolves a google-antigravity row, as internal/registry/models/antigravity_rules.json:
//
//   - the taxonomy (classes, suffix vocabulary) classifyModel reads,
//   - the cascade rules a (provider google-antigravity, api google-gemini-cli)
//     target can match,
//   - the compat keys the `google` record accepts from wire axes,
//   - the compiled google-antigravity variant families (reviewedCollapseTable's
//     source) and the roster exclusions.
//
//   bun capture_antigravity_npi_rules.ts > ../models/antigravity_rules.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json")).default as any;
const { AXES, API_COMPAT_RECORDS } = await import(root + "/pi-catalog/src/compat/axes.ts");

const PROVIDER = "google-antigravity";
const API = "google-gemini-cli";

const googleRecords: string[] = API_COMPAT_RECORDS[API];
const googleWireKeys = Object.values(AXES as Record<string, any>)
	.filter(axis => axis.set === "wire" && axis.records?.some((record: string) => googleRecords.includes(record)))
	.map(axis => axis.key)
	.sort();

const cascade = rules.cascade.rules.filter(
	(rule: any) =>
		(rule.providers === undefined || rule.providers.includes(PROVIDER)) &&
		(rule.apis === undefined || rule.apis.includes(API)) &&
		rule.upstreams === undefined,
);

console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN compat/rules.json",
			taxonomy: {
				classes: rules.taxonomy.classes,
				suffixes: rules.taxonomy.collapse.suffixes,
				pairTokens: rules.taxonomy.collapse.pairTokens,
				lanes: rules.taxonomy.collapse.lanes,
				effortFamilies: rules.taxonomy.collapse.effortFamilies.filter((family: any) => family.provider === PROVIDER),
			},
			cascade,
			googleWireKeys,
			variantFamilies: rules.taxonomy.collapse.variantFamilies.filter((family: any) => family.provider === PROVIDER),
			providerAliases: rules.taxonomy.collapse.providerAliases[PROVIDER] ?? {},
			excludeModels: rules.behavior.excludeModels.filter((rule: any) => rule.provider === PROVIDER),
		},
		null,
		1,
	),
);
