// Exports NeoPi's OWN resolved `google` catalog for NeoProxy's registry
// (internal/registry/models/google_neopi.json): the bundled rows that serve an
// account until its /v1beta/models listing answers, plus the probe listing
// (testdata/gemini_models_probe.json) run through NeoPi's own discovery mapper
// and buildModel, so the Go resolver for rows NeoPi has no bundled reference
// for (internal/googlewire) is pinned to NeoPi's compat rules.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { googleModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/google.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");

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
	...(model.thinking
		? {
				thinking: {
					efforts: model.thinking.efforts,
					mode: model.thinking.mode,
					...(model.thinking.requiresEffort ? { requiresEffort: true } : {}),
					...(model.thinking.suppressWhenOff ? { suppressWhenOff: true } : {}),
					...(model.thinking.effortBudgets ? { effortBudgets: model.thinking.effortBudgets } : {}),
				},
			}
		: {}),
	googleCompat: {
		multimodalFunctionResponse: model.compat.multimodalFunctionResponse,
		requiresSkipThoughtSignature: model.compat.requiresSkipThoughtSignature,
		supportsFunctionPartId: model.compat.supportsFunctionPartId,
	},
});

const bundled = getBundledModels("google").map(project);

const payload = await Bun.file(import.meta.dir + "/gemini_models_probe.json").text();
let requests: any[] = [];
const fakeFetch = (async (input: any, init: any) => {
	requests.push({ url: String(input instanceof Request ? input.url : input), method: init?.method, headers: Object.fromEntries(new Headers(init?.headers)) });
	return new Response(payload, { headers: { "content-type": "application/json" } });
}) as typeof fetch;
const options = googleModelManagerOptions({ apiKey: "AIzaProbeKey", fetch: fakeFetch });
const specs = await options.fetchDynamicModels!();
const discovered = specs.map((spec: any) => project(buildModel(spec)));
console.log(JSON.stringify({ source: 'NeoPi OWN getBundledModels("google") and fetchGeminiModels over testdata/gemini_models_probe.json', discoveryRequests: requests, models: bundled, discovered }, null, 2));
