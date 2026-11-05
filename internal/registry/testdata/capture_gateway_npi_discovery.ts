// Runs NeoPi's OWN gateway model managers over gateway_synthetic_listings.json
// (ids NeoPi has no reference for, billing variants, a judge model, a renamed
// model) and records the rows it builds, for the registry's discovery mapper
// to reproduce. Same fake fetch as capture_gateway_npi_profiles.ts.
//
//   bun --preserve-symlinks capture_gateway_npi_discovery.ts > gateway_npi_discovery.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const dataDir = import.meta.dir;
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const options = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
const gunzip = async (name: string) => new TextDecoder().decode(Bun.gunzipSync(new Uint8Array(await Bun.file(`${dataDir}/${name}.gz`).arrayBuffer())));
const project = (model: any) => ({
	id: model.id,
	name: model.name,
	api: model.api,
	baseUrl: model.baseUrl,
	reasoning: model.reasoning,
	input: model.input,
	contextWindow: model.contextWindow ?? 0,
	maxTokens: model.maxTokens ?? 0,
	...(model.thinking ? { thinking: model.thinking } : {}),
	...(model.api === "openai-responses" ? { compat: model.compat } : {}),
	...(model.api === "anthropic-messages" ? { anthropicCompat: model.compat } : {}),
	...(model.api === "openai-completions" ? { completionsCompat: model.compat } : {}),
	...(model.api === "google-generative-ai" ? { googleCompat: model.compat } : {}),
});
const stencil = await gunzip("gateway_stencil.json");
const synthetic = JSON.parse(await Bun.file(`${dataDir}/gateway_synthetic_listings.json`).text());
const out: any = {};
for (const [provider, manager] of [
	["opencode-go", options.opencodeGoModelManagerOptions],
	["opencode-zen", options.opencodeZenModelManagerOptions],
	["vercel-ai-gateway", options.vercelAiGatewayModelManagerOptions],
] as any) {
	const body = JSON.stringify(synthetic[provider]);
	const fakeFetch = (async (input: any) => {
		const url = String(input instanceof Request ? input.url : input);
		if (url === "https://catalog.stencil.so/models.json.zstd") return new Response(stencil, { headers: { "content-type": "application/json" } });
		if (/\/v1\/models(\?|$)/.test(url)) return new Response(body, { headers: { "content-type": "application/json" } });
		return new Response("not found", { status: 404 });
	}) as typeof fetch;
	const opts = manager({ apiKey: "capture-key", fetch: fakeFetch });
	opts.cacheDbPath = `/tmp/gateway-discovery-${provider}-${process.pid}.db`;
	const result = await resolveProviderModels(opts, "online");
	if (result.source !== "provider") throw new Error(`${provider}: discovery did not answer (${result.source})`);
	out[provider] = result.models.filter((model: any) => model.api !== "typesafe").map(project);
}
console.log(JSON.stringify({ source: "NeoPi OWN resolveProviderModels over gateway_synthetic_listings.json", providers: out }, null, 1));
