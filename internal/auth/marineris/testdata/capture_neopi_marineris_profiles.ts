// Exports how NeoPi's OWN catalog engine (pi-catalog buildModel) resolves the
// models its Marineris extension registers, for NeoProxy's registry
// (internal/registry/models/marineris_neopi.json).
//
// The gateway's roster is whatever the key is scoped to, so the export covers
// every xAI and Codex model id NeoPi's catalog knows plus per-class probes:
// each id is fed through the extension's own fetchDynamicModels mapping and
// then built with provider "marineris" at the gateway address. NeoProxy does
// not port the catalog's rule engine; unknown ids use the class probe.
//
//   HOME=$(mktemp -d) NEOPI_SOURCE_MODULES=<...>/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_marineris_profiles.ts > ../../../registry/models/marineris_neopi.json
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
if (!homedir().startsWith(tmpdir() + "/")) throw new Error("run with HOME set to a scratch directory under " + tmpdir());
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
// The catalog's compat depends on whether the gateway is a local-network
// server (loopback, RFC 1918, .local), so both classes are exported.
const HOSTS = { local: "http://192.168.50.50:8801/v1", public: "https://gw.example.invalid/v1" } as const;

let rows: unknown[] = [];
const originalFetch = globalThis.fetch;
(globalThis as any).fetch = Object.assign(async () => Response.json({ data: rows }), { preconnect: originalFetch.preconnect });
let provider: any;
const extension = (await import(join(import.meta.dir, "neopi-extension", "marineris", "index.ts"))) as { default: (pi: any) => void };
extension.default({ registerProvider: (_id: string, config: any) => (provider = config) });

const sources: Record<string, string> = {};
for (const model of getBundledModels("xai")) sources[model.id] = "xai";
for (const model of getBundledModels("openai-codex")) sources[model.id] = "codex";
// Ids the gateway may list that NeoPi's catalog does not carry yet.
for (const id of ["o3", "o4-mini", "codex-mini", "gpt-5", "gpt-4.1", "grok-3-mini", "grok-4.6", "gpt-6.1-sol", "gpt-6.1-terra"]) sources[id] ??= id.startsWith("grok") ? "xai" : "codex";
const ids = Object.keys(sources).sort();
// Every id prefix that ends at a "-" boundary is a family NeoPi's rules may
// classify; an unknown id resolves as the longest family prefix it starts with.
// "o" and "codex" are the extension's own Codex heuristics for ids with no family.
const prefixes = [...new Set(["codex", "gpt-", ...ids.flatMap(id => id.split("-").slice(0, -1).map((_, i, parts) => id.split("-").slice(0, i + 1).join("-") + "-"))])].sort();
// "o" is the extension's own Codex heuristic for ids with no known family; a
// name that is not an o-series id stands for it.
const families: { prefix: string; stem: string }[] = [
	{ prefix: "", stem: "zz-probe" },
	{ prefix: "o", stem: "orbit-zz-probe" },
	...prefixes.map(prefix => ({ prefix, stem: prefix + "zz-probe" })),
];
const probes = families.flatMap(({ stem }) => [stem, stem + "-reasoning"]);

const map = async (list: unknown[]) => {
	rows = list;
	return (await provider.fetchDynamicModels("mrn_probe")) as any[];
};
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
const resolve = (spec: any, baseUrl: string) => {
	const built = buildModel({ ...spec, provider: "marineris", baseUrl });
	const thinking = built.thinking ? { ...built.thinking } : undefined;
	if (thinking) delete thinking.requiresEffort;
	return { built, thinking, profile: profileOf(built) };
};

const specs = await map([...ids.map(id => ({ id, owned_by: sources[id] })), ...probes.map(id => ({ id }))]);
const byId = new Map(specs.map(spec => [spec.id, spec]));
const hosts: Record<string, { classes: unknown[]; models: unknown[] }> = {};
for (const [host, baseUrl] of Object.entries(HOSTS)) {
	const models = ids.map(id => {
		const { built, profile } = resolve(byId.get(id), baseUrl);
		return { id, api: built.api, profile };
	});
	const classes: { prefix: string; api: string; reasoning: boolean; profile: string }[] = [];
	for (const { prefix, stem } of families) {
		for (const id of [stem, stem + "-reasoning"]) {
			const { built, profile } = resolve(byId.get(id), baseUrl);
			classes.push({ prefix, api: built.api, reasoning: built.reasoning, profile });
		}
	}
	hosts[host] = { classes, models };
}
console.log(JSON.stringify({ source: "NeoPi OWN pi-catalog buildModel over the marineris extension's mapping", hostExamples: HOSTS, profiles, hosts }, null, 1));
