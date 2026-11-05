// Exports NeoPi's OWN resolved OpenRouter chat catalog for NeoProxy's registry
// (internal/registry/models/openrouter_neopi.json.gz): getBundledModels("openrouter")
// rows on api "openrouter" (the chat roster; image, video, rerank, decisions,
// embedding and transcription rows are other kinds), with the compat NeoPi's
// buildModel resolved. Optionally (OPENROUTER_ROSTER=<file>) also runs NeoPi's
// OWN openrouterModelManagerOptions().fetchDynamicModels against that recorded
// /models roster served from loopback, then resolveProviderModels' merge, and
// exports the merged chat rows as "discovered" for the discovery parity test.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

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
	...(model.requestModelId ? { requestModelId: model.requestModelId } : {}),
	...(model.thinking ? { thinking: model.thinking } : {}),
	compat: model.compat,
});
const chat = (model: any) => model.api === "openrouter" && (model.kind === undefined || model.kind === "chat");
const all = getBundledModels("openrouter");
const models = all.filter(chat).map(project);
// Ids the bundle serves as a non-chat runner (image, video, embedding, rerank,
// decisions, transcription): mergeDynamicModels never replaces those with a
// discovered chat row.
const nonChatIds = all.filter((model: any) => !chat(model)).map((model: any) => model.id);
const out: any = { source: "NeoPi OWN getBundledModels(\"openrouter\"), api openrouter chat rows", models, nonChatIds };

const rosterPath = process.env.OPENROUTER_ROSTER;
if (rosterPath) {
	const roster = await Bun.file(rosterPath).text();
	const server = Bun.serve({
		hostname: "127.0.0.1",
		port: 0,
		fetch(request) {
			const url = new URL(request.url);
			if (url.pathname === "/api/v1/models" && !url.search) return new Response(roster, { headers: { "content-type": "application/json" } });
			return new Response(JSON.stringify({ data: [] }), { headers: { "content-type": "application/json" } });
		},
	});
	try {
		const { openrouterModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
		const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
		// NeoPi resolves compat by host, so discovery runs against the official base URL
		// and the fetch redirect keeps the traffic on the loopback roster.
		const redirect = (async (input: any, init: any) => {
			const target = new URL(input instanceof Request ? input.url : String(input));
			return fetch(`http://127.0.0.1:${server.port}${target.pathname}${target.search}`, init);
		}) as typeof fetch;
		const options = openrouterModelManagerOptions({ apiKey: "sk-or-v1-capture", fetch: redirect });
		const fetched = (await options.fetchDynamicModels()) ?? [];
		out.fetchedCount = fetched.length;
		// The chat fetch's raw specs, before the model manager merges them with the bundle.
		out.fetched = fetched.filter((model: any) => model.api === "openrouter").map((model: any) => ({ id: model.id, name: model.name, reasoning: model.reasoning, input: model.input, contextWindow: model.contextWindow, maxTokens: model.maxTokens, cost: model.cost, thinking: model.thinking, compat: model.compat }));
		const dir = (await import("node:fs")).mkdtempSync("/tmp/or-capture-");
		const resolved = await resolveProviderModels({ ...options, cacheDbPath: dir + "/models.db" }, "online");
		out.resolvedSource = resolved.source;
		out.resolved = resolved.models.filter(chat).map(project);
	} finally {
		server.stop(true);
	}
}
console.log(JSON.stringify(out));
