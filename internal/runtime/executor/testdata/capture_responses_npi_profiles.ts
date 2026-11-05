// Exports NeoPi's OWN resolved catalogs for the openai-responses providers the
// NeoProxy `responses` lane ports (meta, muse-code, sakana, abliteration), and
// the model manager's discovery results over scripted /v1/models rosters, as
// internal/registry/models/responses_neopi.json. The rosters run through
// NeoPi's real provider model managers (resolveProviderModels) against a
// loopback server, so Go's merge/mapping rules are pinned to NeoPi's output.
//
// Default output is the full fixture (responses_npi_profiles.json). With
// `--registry` it prints only {source, seeds, templates}, the embedded
// internal/registry/models/responses_neopi.json.
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const { isExcludedModel } = await import(root + "/pi-catalog/src/compat/behavior.ts");
const { metaModelManagerOptions, museCodeModelManagerOptions, sakanaModelManagerOptions, abliterationModelManagerOptions } =
	await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");

export const project = (model: any) => ({
	id: model.id,
	name: model.name,
	api: model.api,
	provider: model.provider,
	baseUrl: model.baseUrl,
	reasoning: model.reasoning,
	input: model.input,
	contextWindow: model.contextWindow ?? 0,
	maxTokens: model.maxTokens ?? 0,
	...(model.thinking
		? {
				thinking: {
					efforts: model.thinking.efforts,
					...(model.thinking.effortMap ? { effortMap: model.thinking.effortMap } : {}),
					...(model.thinking.mode ? { mode: model.thinking.mode } : {}),
					...(model.thinking.requiresEffort ? { requiresEffort: true } : {}),
					...(model.thinking.defaultLevel ? { defaultLevel: model.thinking.defaultLevel } : {}),
				},
			}
		: {}),
	compat: model.compat,
});

const providers = ["meta", "muse-code", "sakana", "abliteration"];
const managers: Record<string, (config: any) => any> = {
	meta: metaModelManagerOptions,
	"muse-code": museCodeModelManagerOptions,
	sakana: sakanaModelManagerOptions,
	abliteration: abliterationModelManagerOptions,
};

let roster: any[] = [];
const requests: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const headers = Object.fromEntries(request.headers);
		delete headers.host; // loopback port, not part of the wire contract
		requests.push({ method: request.method, path: url.pathname + url.search, headers });
		if (url.pathname === "/v1/models") return Response.json({ object: "list", data: roster });
		return new Response("not found", { status: 404 });
	},
});
const local = `http://127.0.0.1:${server.port}`;
const redirectFetch = (async (input: any, init: any) => {
	const target = new URL(input instanceof Request ? input.url : String(input));
	return fetch(local + target.pathname + target.search, init);
}) as typeof fetch;

const bundled: Record<string, any[]> = {};
for (const provider of providers) bundled[provider] = getBundledModels(provider).map(project);

const entry = (id: string, extra: Record<string, unknown> = {}) => ({ id, object: "model", created: 1, owned_by: "x", ...extra });
const rosters: Record<string, { name: string; roster: any[] }[]> = {
	meta: [
		{ name: "seeded", roster: ["muse-spark-1.1", "muse-spark-1.2", "muse-spark-1.2-contributor", "muse-spark-1.3", "muse-spark-1.3-contributor", "muse-wonder-1.0"].map(id => entry(id)) },
		{ name: "partial", roster: [entry("muse-spark-1.3")] },
		{
			name: "new-and-unknown",
			roster: [
				entry("muse-spark-1.3", { context_length: 2000000, max_completion_tokens: 64000, name: "Spark Three" }),
				entry("muse-spark-1.4"),
				entry("muse-spark-1.4-contributor"),
				entry("muse-spark-2.0"),
				entry("muse-wonder-2.0"),
				entry("muse-spark-pro"),
				entry("muse-spark-2.0-contributor"),
				entry("muse-spark-1.3.1"),
				entry("muse-future-9"),
				entry("muse-image-1"),
				entry("muse-voice-1"),
				entry("sam-3"),
			],
		},
		{ name: "empty", roster: [] },
	],
	"muse-code": [
		{ name: "seeded", roster: ["muse-spark-1.3", "muse-spark-1.3-contributor", "muse-wonder-1.0", "muse-wonder-1.0-contributor", "muse-wonder-full-1.0", "muse-wonder-full-1.0-contributor"].map(id => entry(id)) },
		{ name: "new-and-unknown", roster: [entry("muse-spark-1.4"), entry("muse-spark-1.4-contributor"), entry("muse-future-9"), entry("muse-image-1"), entry("sam-3"), entry("muse-wonder-1.0")] },
	],
	sakana: [
		{ name: "seeded", roster: ["fugu", "fugu-ultra", "fugu-ultra-20260615"].map(id => entry(id)) },
		{ name: "new-and-unknown", roster: [entry("fugu"), entry("fugu-pro"), entry("Fugu-Mini-2"), entry("other-model", { context_length: 8192 })] },
		{ name: "empty", roster: [] },
	],
	abliteration: [
		{ name: "seeded", roster: ["abliterated-model", "abliterated-model-large", "abliterated-model-large-v2"].map(id => entry(id)) },
		{ name: "new-and-unknown", roster: [entry("abliterated-model"), entry("abliterated-model-large-v3"), entry("something-else", { context_length: 4096, max_completion_tokens: 512 })] },
		{ name: "empty", roster: [] },
	],
};

const discovery: any[] = [];
for (const provider of providers) {
	for (const scenario of rosters[provider]) {
		roster = scenario.roster;
		requests.length = 0;
		const dir = mkdtempSync(join(tmpdir(), "responses-npi-"));
		const options = managers[provider]({ apiKey: "discovery-key", fetch: redirectFetch });
		const result = await resolveProviderModels({ ...options, cacheDbPath: join(dir, "cache.db") }, "online");
		discovery.push({
			provider,
			name: scenario.name,
			roster: scenario.roster.map((model: any) => model.id),
			rosterRaw: scenario.roster,
			source: result.source,
			requests: requests.filter(request => request.path.startsWith("/v1/models")),
			models: result.models.map(project),
		});
	}
}
// Template rows: what a roster id with no seed row becomes, per category. The
// Go registry clones these and swaps the id/name.
const templateProbes: Record<string, Record<string, string>> = {
	meta: { unknown: "zz-unknown-probe", bounded: "muse-wonder-9.9" },
	"muse-code": { unknown: "zz-unknown-probe", bounded: "muse-wonder-9.9" },
	sakana: { fugu: "fugu-template-probe", plain: "zz-plain-probe" },
	abliteration: { unseeded: "zz-unseeded-probe" },
};
const templates: Record<string, Record<string, any>> = {};
for (const provider of providers) {
	roster = Object.values(templateProbes[provider]).map(id => entry(id));
	const dir = mkdtempSync(join(tmpdir(), "responses-npi-"));
	const options = managers[provider]({ apiKey: "discovery-key", fetch: redirectFetch });
	const result = await resolveProviderModels({ ...options, cacheDbPath: join(dir, "cache.db") }, "online");
	templates[provider] = {};
	for (const [category, id] of Object.entries(templateProbes[provider])) {
		templates[provider][category] = project(result.models.find((model: any) => model.id === id));
	}
}
const probes = ["muse-image-1", "muse-voice-1", "sam-3", "muse-spark-1.3"];
const excluded = Object.fromEntries(["meta", "muse-code"].map(provider => [provider, Object.fromEntries(probes.map(id => [id, isExcludedModel(provider, id)]))]));
const seeds: Record<string, any[]> = {};
{
	const { seedModels } = await import(root + "/pi-catalog/src/compat/providers.ts");
	// Meta and Muse Code merge their KDL seed rows (staticModels); Sakana and
	// Abliteration merge the bundled catalog, whose order differs from the KDL.
	for (const provider of providers) {
		seeds[provider] =
			provider === "sakana" || provider === "abliteration"
				? getBundledModels(provider).map(project)
				: seedModels(provider).map((spec: any) => project(buildModel(spec)));
	}
}
server.stop(true);
const source = "NeoPi OWN seedModels + buildModel; provider model managers (resolveProviderModels)";
console.log(JSON.stringify(process.argv.includes("--registry") ? { source, seeds, templates } : { source, seeds, bundled, excluded, templates, discovery }, null, 2));
