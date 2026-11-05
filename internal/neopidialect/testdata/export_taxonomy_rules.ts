// Exports the slice of NeoPi's model taxonomy (catalog compat/rules.json) that
// preferredDialect() reads: per-class matchers, per-class identity overrides and
// the provider-agnostic collapse suffixes. Output is embedded by the Go package as
// taxonomy_rules.json.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks export_taxonomy_rules.ts > taxonomy_rules.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;

const classes = rules.taxonomy.classes.map((cls: any) => ({
	id: cls.id,
	matchers: cls.matchers.map((m: any) => ({ kind: m.kind, token: m.token, ...(m.bounded ? { bounded: true } : {}) })),
	overrides: cls.overrides.map((o: any) => ({
		...(o.model !== undefined ? { model: o.model } : {}),
		...(o.glob !== undefined ? { glob: o.glob } : {}),
		...(o.provider !== undefined ? { provider: o.provider } : {}),
		...(o.logical !== undefined ? { logical: o.logical } : {}),
		...(o.class !== undefined ? { class: o.class } : {}),
		...(o.expiresAtMs !== undefined ? { expiresAtMs: o.expiresAtMs } : {}),
	})),
}));
const suffixes = rules.taxonomy.collapse.suffixes.map((s: any) => ({
	suffix: s.suffix,
	...(s.exceptBarePrefix !== undefined ? { exceptBarePrefix: s.exceptBarePrefix } : {}),
}));
console.log(JSON.stringify({ classes, suffixes }));
