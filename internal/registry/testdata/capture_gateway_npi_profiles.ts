// Exports NeoPi's OWN resolved gateway catalogs (OpenCode Go, OpenCode Zen,
// Vercel AI Gateway) for NeoProxy's registry (internal/registry/models/
// gateway_neopi.json.gz). Each provider's model manager runs through
// resolveProviderModels exactly as NeoPi's model discovery does, against a
// recorded live /models listing (gateway_<provider>_models_live.json.gz) and a
// recorded stencil.so models.dev copy served by a fake fetch: the bundled
// rows, route pins and compat rules apply as in NeoPi.
//
// Probe ids ("zz-...") ride in the listing to capture the row NeoPi builds for
// a model it has no reference for; the registry keeps them as templates for
// ids a later live listing adds.
//
//   cd <neopi env> && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_gateway_npi_profiles.ts | gzip -9 > gateway_neopi.json.gz
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const dataDir = import.meta.dir;
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const options = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
const gunzip = async (name: string) => new TextDecoder().decode(Bun.gunzipSync(new Uint8Array(await Bun.file(`${dataDir}/${name}.gz`).arrayBuffer())));
const project = (model: any) => ({
	id: model.id,
	name: model.name,
	api: model.api,
	provider: model.provider,
	baseUrl: model.baseUrl,
	reasoning: model.reasoning,
	input: model.input,
	contextWindow: model.contextWindow ?? 0,
	maxTokens: model.maxTokens ?? 0,
	...(model.thinking ? { thinking: model.thinking } : {}),
	...(model.requestModelId ? { requestModelId: model.requestModelId } : {}),
	...(model.api === "openai-responses" ? { compat: model.compat } : {}),
	...(model.api === "anthropic-messages" ? { anthropicCompat: model.compat } : {}),
	...(model.api === "openai-completions" ? { completionsCompat: model.compat } : {}),
	...(model.api === "google-generative-ai" ? { googleCompat: model.compat } : {}),
});
const stencil = await gunzip("gateway_stencil.json");
const probes: Record<string, any[]> = {
	"opencode-go": [
		{ id: "zz-unknown-model", name: "zz-unknown-model" },
		{ id: "muse-spark-9.9", name: "muse-spark-9.9" },
		{ id: "union-alpha-free", name: "union-alpha-free" },
		{ id: "gemini-4-argon-free", name: "gemini-4-argon-free" },
	],
	"opencode-zen": [
		{ id: "zz-unknown-model", name: "zz-unknown-model" },
		{ id: "muse-spark-9.9", name: "muse-spark-9.9" },
		{ id: "union-alpha-free", name: "union-alpha-free" },
		{ id: "gemini-4-argon-free", name: "gemini-4-argon-free" },
	],
	"vercel-ai-gateway": [
		{ id: "zz-unknown/alpha-model", name: "zz alpha", tags: ["tool-use", "reasoning"], context_window: 100000, max_tokens: 8000 },
		{ id: "zz-unknown/beta-model", name: "zz beta", tags: ["tool-use"], context_window: 100000, max_tokens: 8000 },
	],
};
const providers: any = {};
const templates: any = {};
const bundled: any = {};
const dropped: any = {};
const listed: any = {};
for (const [provider, manager] of [
	["opencode-go", options.opencodeGoModelManagerOptions],
	["opencode-zen", options.opencodeZenModelManagerOptions],
	["vercel-ai-gateway", options.vercelAiGatewayModelManagerOptions],
] as any) {
	const listing = JSON.parse(await gunzip(`gateway_${provider}_models_live.json`));
	listing.data.push(...probes[provider]);
	const body = JSON.stringify(listing);
	// The live roster and the stencil.so models.dev copy are the only remote sources.
	const fakeFetch = (async (input: any) => {
		const url = String(input instanceof Request ? input.url : input);
		if (url === "https://catalog.stencil.so/models.json.zstd") return new Response(stencil, { headers: { "content-type": "application/json" } });
		if (/\/v1\/models(\?|$)/.test(url)) return new Response(body, { headers: { "content-type": "application/json" } });
		return new Response("not found", { status: 404 });
	}) as typeof fetch;
	const opts = manager({ apiKey: "capture-key", fetch: fakeFetch });
	opts.cacheDbPath = `/tmp/gateway-capture-${provider}-${process.pid}.db`;
	const result = await resolveProviderModels(opts, "online");
	if (result.source !== "provider") throw new Error(`${provider}: discovery did not answer (${result.source})`);
	const rows = result.models.filter((model: any) => model.api !== "typesafe");
	// Probe ids are the live listing's own ids when they are real models.
	const real = new Set(JSON.parse(await gunzip(`gateway_${provider}_models_live.json`)).data.map((entry: any) => entry.id));
	const probe = (model: any) => probes[provider].some(candidate => candidate.id === model.id) && !real.has(model.id);
	providers[provider] = rows.filter((model: any) => !probe(model)).map(project);
	templates[provider] = Object.fromEntries(rows.filter(probe).map((model: any) => [model.id, project(model)]));
	// NeoPi's bundled catalog as an account with no discovery serves it (no
	// API key, so no dynamic fetch): the seed, and the reference rows
	// discovery draws a model's API and metadata from.
	const seedFetch = (async (input: any) => {
		const url = String(input instanceof Request ? input.url : input);
		if (url === "https://catalog.stencil.so/models.json.zstd") return new Response(stencil, { headers: { "content-type": "application/json" } });
		return new Response("not found", { status: 404 });
	}) as typeof fetch;
	const offline = manager({ fetch: seedFetch });
	offline.cacheDbPath = `/tmp/gateway-capture-${provider}-offline-${process.pid}.db`;
	const seed = await resolveProviderModels(offline, "online");
	bundled[provider] = seed.models.filter((model: any) => model.api !== "typesafe").map(project);
	// Listed ids NeoPi's variant collapse folds into another id (or otherwise
	// drops) never become models of their own.
	const kept = new Set(rows.map((model: any) => model.id));
	const roster: any[] = JSON.parse(await gunzip(`gateway_${provider}_models_live.json`)).data;
	// The values the listing itself reported per id, so a later listing's
	// changes can be told apart from the recorded merge.
	listed[provider] = Object.fromEntries(roster.map((entry: any) => [entry.id, {
		name: entry.name ?? null,
		contextWindow: typeof (entry.context_window ?? entry.context_length) === "number" ? (entry.context_window ?? entry.context_length) : 0,
		maxTokens: typeof (entry.max_tokens ?? entry.max_completion_tokens) === "number" ? (entry.max_tokens ?? entry.max_completion_tokens) : 0,
		tags: Array.isArray(entry.tags) ? entry.tags : [],
	}]));
	dropped[provider] = JSON.parse(await gunzip(`gateway_${provider}_models_live.json`)).data.map((entry: any) => entry.id).filter((id: string) => !kept.has(id));
}
console.log(JSON.stringify({ source: "NeoPi OWN resolveProviderModels over the recorded live listings", providers, bundled, listed, dropped, templates }));
