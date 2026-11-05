// Exports how NeoPi's OWN catalog engine (pi-catalog buildModel) resolves
// models the Nous Portal extension registers, for NeoProxy's registry
// (internal/registry/models/nous_neopi.json).
//
// The extension maps /v1/models rows to openai-completions specs; NeoPi then
// builds each with provider "nous-portal" at the inference base URL, which
// resolves the request compat (ResolvedOpenAICompat) and the effort ladder
// from the model's identity. NeoProxy does not port that rule engine, so the
// export carries:
//   - one resolved record per row of the extension's bundled snapshot.json,
//   - NeoPi's answer for an unknown model of each namespace the snapshot has
//     (and for a wholly unknown vendor), with reasoning off and on,
// and the extension's enrichment.json verbatim for its mapRow fallbacks.
//
//   NEOPI_SOURCE_MODULES=<...>/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_nous_profiles.ts > ../../../registry/models/nous_neopi.json
import { join } from "node:path";
import { ensureScratchHome } from "../../marineris/testdata/scratch_home.ts";
await ensureScratchHome();

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const dir = join(import.meta.dir, "neopi-extension", "nous-portal");
const snapshot = JSON.parse(await Bun.file(join(dir, "snapshot.json")).text()) as any[];
const enrichment = JSON.parse(await Bun.file(join(dir, "enrichment.json")).text());
const BASE_URL = "https://inference-api.nousresearch.com/v1";

const build = (spec: any) => buildModel({ ...spec, provider: "nous-portal", baseUrl: BASE_URL });

// Profiles are the policy-resolved pieces that vary by model identity.
const profiles: Record<string, { compat: unknown; requiresEffort?: boolean }> = {};
const profileOf = (model: any): string => {
	const profile: { compat: unknown; requiresEffort?: boolean } = { compat: model.compat };
	if (model.thinking?.requiresEffort) profile.requiresEffort = true;
	const key = JSON.stringify(profile);
	const existing = Object.entries(profiles).find(([, value]) => JSON.stringify(value) === key);
	if (existing) return existing[0];
	const id = "p" + Object.keys(profiles).length;
	profiles[id] = profile;
	return id;
};

const models = snapshot.map(row => {
	const model = build(row);
	// What the policy adds to the spec's own effort ladder (kimi-k3 gains an
	// effort map and a default); nothing else changes on the snapshot rows.
	const added: Record<string, unknown> = {};
	for (const [key, value] of Object.entries(model.thinking ?? {})) {
		if (key === "requiresEffort" || JSON.stringify(value) === JSON.stringify((row.thinking ?? {})[key])) continue;
		added[key] = value;
	}
	const thinking = model.thinking ? { ...model.thinking } : undefined;
	if (thinking) delete thinking.requiresEffort;
	return {
		...(Object.keys(added).length > 0 && row.thinking ? { thinkingAdded: added } : {}),
		id: row.id,
		name: model.name,
		api: model.api,
		reasoning: model.reasoning,
		input: model.input,
		...(row.supportsTools === false ? { supportsTools: false } : {}),
		contextWindow: model.contextWindow,
		maxTokens: model.maxTokens,
		...(thinking ? { thinking } : {}),
		profile: profileOf(model),
	};
});

const probe = (id: string, reasoning: boolean) => ({
	id,
	name: id,
	api: "openai-completions",
	reasoning,
	input: ["text"],
	cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
	contextWindow: 131072,
	maxTokens: 32768,
	compat: { supportsStore: false, supportsDeveloperRole: false, maxTokensField: "max_tokens", ...(reasoning ? { thinkingFormat: "openrouter" } : {}) },
});
const namespaceOf = (id: string) => id.replace(/^~/, "").split("/")[0];
const namespaces = [...new Set(snapshot.filter(row => row.id.includes("/")).map(row => namespaceOf(row.id)))].sort();
const fallbacks: Record<string, { off: string; on: string }> = {};
// "thinking" is the ladder NeoPi gives an unknown reasoning model of the
// namespace (its effort ladder is the policy's, as the spec carries none).
const unknown = (ns: string) => {
	const on = build(probe(`${ns}/probe-model`, true));
	const thinking = on.thinking ? { ...on.thinking } : undefined;
	if (thinking) delete thinking.requiresEffort;
	return { off: profileOf(build(probe(`${ns}/probe-model`, false))), on: profileOf(on), ...(thinking ? { thinking } : {}) };
};
const defaults = unknown("unknown-vendor");
for (const ns of namespaces) {
	const resolved = unknown(ns);
	if (JSON.stringify(resolved) !== JSON.stringify(defaults)) fallbacks[ns] = resolved;
}

console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN pi-catalog buildModel over the nous-portal extension's snapshot.json and per-namespace probes",
			profiles,
			default: defaults,
			namespaces: fallbacks,
			models,
			enrichment,
		},
		null,
		1,
	),
);
