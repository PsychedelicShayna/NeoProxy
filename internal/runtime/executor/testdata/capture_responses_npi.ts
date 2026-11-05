// Runs NeoPi's OWN openai-responses provider (streamOpenAIResponses) for the
// meta, muse-code, sakana and abliteration models against a loopback capture
// server only; no request can reach those hosts. Each scenario records the
// request NeoPi sends (method, URL, headers, raw body) plus the equivalent
// downstream Responses request NeoProxy receives, so the Go port can replay it.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamOpenAIResponses } = await import(root + "/pi-ai/src/providers/openai-responses.ts");
const { getProviderDefinition } = await import(root + "/pi-ai/src/registry/registry.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { seedModels } = await import(root + "/pi-catalog/src/compat/providers.ts");
const { clampThinkingLevelForModel } = await import(root + "/pi-catalog/src/model-thinking.ts");
const { USER_AGENT } = await import(root + "/pi-utils/src/dirs.ts");

const sessionId = "capture-session-0001";
const events = [
	{ type: "response.created", response: { id: "resp_capture", status: "in_progress", output: [] } },
	{ type: "response.output_item.added", output_index: 0, item: { id: "msg_capture", type: "message", role: "assistant", status: "in_progress", content: [] } },
	{ type: "response.content_part.added", item_id: "msg_capture", output_index: 0, content_index: 0, part: { type: "output_text", text: "", annotations: [] } },
	{ type: "response.output_text.delta", item_id: "msg_capture", output_index: 0, content_index: 0, delta: "Captured." },
	{ type: "response.output_text.done", item_id: "msg_capture", output_index: 0, content_index: 0, text: "Captured." },
	{ type: "response.output_item.done", output_index: 0, item: { id: "msg_capture", type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text: "Captured.", annotations: [] }] } },
	{ type: "response.completed", response: { id: "resp_capture", status: "completed", output: [{ id: "msg_capture", type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text: "Captured.", annotations: [] }] }], usage: { input_tokens: 3, output_tokens: 1, total_tokens: 4 } } },
];
const sse = events.map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join("");
let captured: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody: await request.text() });
		return new Response(sse, { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;

const museCredential = JSON.stringify({ oauthAccessToken: "meta-account-access", apiKey: "LLM|subscription-key" });
// Credentials as NeoPi stores them: raw keys, and the muse-code JSON bearer.
const credentials: Record<string, string> = {
	meta: "LLM|payg-key",
	"muse-code": museCredential,
	sakana: "sakana-capture-key",
	abliteration: "ak_capture_key",
};

const seed = (provider: string, id: string) => {
	const found = [...getBundledModels(provider), ...seedModels(provider)].find((model: any) => model.id === id);
	if (!found) throw new Error(`missing ${provider} model ${id}`);
	return found;
};
const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const lookupWire = { type: "function", ...lookup };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const assistant = (provider: string, model: string, content: unknown[], items: unknown[]) => ({ role: "assistant", content, api: "openai-responses", provider, model, usage, stopReason: "toolUse", timestamp: 1, providerPayload: { type: "openaiResponsesHistory", provider, dt: true, items } });
const userText = (text: string) => ({ role: "user", content: [{ type: "input_text", text }] });
const reasoningItem = { type: "reasoning", id: "rs_capture", summary: [], encrypted_content: "enc-capture" };
const callItem = { type: "function_call", id: "fc_capture", call_id: "call_1", name: "lookup", arguments: "{\"term\":\"a\"}" };
const imageB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";

type Spec = { name: string; provider: string; model: string; options: any; tools?: any[]; systemPrompt?: string[]; messages: any[]; downstream: any };
const scenarios: Spec[] = [];
const plain = (provider: string, model: string, effort: string | undefined, extra: Partial<Spec> = {}, label = effort ?? "no-effort"): Spec => ({
	name: `${provider}-${model}-${label}`,
	provider,
	model,
	options: effort ? { reasoning: effort } : {},
	messages: [{ role: "user", content: "Say hello." }],
	downstream: { model, input: [userText("Say hello.")], ...(effort ? { reasoning: { effort } } : {}) },
	...extra,
});
for (const effort of [undefined, "minimal", "low", "medium", "high", "xhigh", "max"]) {
	scenarios.push(plain("meta", "muse-spark-1.3", effort));
	scenarios.push(plain("muse-code", "muse-spark-1.3", effort));
}
for (const effort of ["minimal", "high", "xhigh", "max"]) scenarios.push(plain("meta", "muse-spark-1.1", effort));
for (const effort of [undefined, "high", "xhigh", "max"]) {
	scenarios.push(plain("muse-code", "muse-wonder-full-1.0", effort));
	scenarios.push(plain("meta", "muse-wonder-1.0", effort));
	scenarios.push(plain("sakana", "fugu", effort));
	scenarios.push(plain("sakana", "fugu-ultra", effort));
}
scenarios.push(plain("sakana", "fugu", "low", {}, "unsupported-low"));
for (const model of ["abliterated-model", "abliterated-model-large", "abliterated-model-large-v2"]) {
	for (const effort of [undefined, "minimal", "low", "medium", "high", "xhigh", "max"]) scenarios.push(plain("abliteration", model, effort));
}
for (const provider of ["meta", "muse-code", "sakana", "abliteration"]) {
	const model = { meta: "muse-spark-1.3", "muse-code": "muse-spark-1.3-contributor", sakana: "fugu", abliteration: "abliterated-model" }[provider]!;
	scenarios.push({
		name: `${provider}-system-sampling`, provider, model, options: { reasoning: "high", temperature: 0.2, topP: 0.9, maxTokens: 99 }, systemPrompt: ["You are terse."],
		messages: [{ role: "user", content: "Say hello." }],
		downstream: { model, instructions: "You are terse.", input: [userText("Say hello.")], reasoning: { effort: "high" }, stream: true, temperature: 0.2, top_p: 0.9, max_output_tokens: 99, store: true },
	});
	scenarios.push({
		name: `${provider}-tools-auto`, provider, model, options: { reasoning: "high", toolChoice: "auto" }, tools: [lookup],
		messages: [{ role: "user", content: "Use lookup." }],
		downstream: { model, input: [userText("Use lookup.")], tools: [lookupWire], tool_choice: "auto", reasoning: { effort: "high" } },
	});
	for (const choice of ["required", "none"]) {
		scenarios.push({
			name: `${provider}-tools-${choice}`, provider, model, options: { reasoning: "high", toolChoice: choice }, tools: [lookup],
			messages: [{ role: "user", content: "Use lookup." }],
			downstream: { model, input: [userText("Use lookup.")], tools: [lookupWire], tool_choice: choice, reasoning: { effort: "high" } },
		});
	}
	scenarios.push({
		name: `${provider}-tools-named`, provider, model, options: { reasoning: "high", toolChoice: { type: "tool", name: "lookup" } }, tools: [lookup],
		messages: [{ role: "user", content: "Use lookup." }],
		downstream: { model, input: [userText("Use lookup.")], tools: [lookupWire], tool_choice: { type: "function", name: "lookup" }, reasoning: { effort: "high" } },
	});
	scenarios.push({
		name: `${provider}-tool-history-replay`, provider, model, options: { reasoning: "high" }, tools: [lookup],
		messages: [
			{ role: "user", content: "Use lookup." },
			assistant(provider, model, [{ type: "toolCall", id: "call_1|fc_capture", name: "lookup", arguments: { term: "a" } }], [reasoningItem, callItem]),
			{ role: "toolResult", toolCallId: "call_1|fc_capture", toolName: "lookup", content: [{ type: "text", text: "Found." }], isError: false, timestamp: 2 },
		],
		downstream: { model, input: [userText("Use lookup."), reasoningItem, callItem, { type: "function_call_output", call_id: "call_1", output: "Found." }], tools: [lookupWire], reasoning: { effort: "high" } },
	});
	scenarios.push({
		name: `${provider}-max-output-clamp`, provider, model, options: { maxTokens: 900000 }, messages: [{ role: "user", content: "Long." }],
		downstream: { model, input: [userText("Long.")], max_output_tokens: 900000 },
	});
	scenarios.push({
		name: `${provider}-summary-null`, provider, model, options: { reasoning: "high", reasoningSummary: null }, messages: [{ role: "user", content: "Quiet." }],
		downstream: { model, input: [userText("Quiet.")], reasoning: { effort: "high", summary: null } },
	});
	scenarios.push({
		name: `${provider}-reasoning-none`, provider, model, options: { disableReasoning: true }, messages: [{ role: "user", content: "Fast." }],
		downstream: { model, input: [userText("Fast.")], reasoning: { effort: "none" } },
	});
	scenarios.push({
		name: `${provider}-image-input`, provider, model, options: {}, messages: [{ role: "user", content: [{ type: "text", text: "Describe." }, { type: "image", mimeType: "image/png", data: imageB64 }] }],
		downstream: { model, input: [{ role: "user", content: [{ type: "input_text", text: "Describe." }, { type: "input_image", detail: "auto", image_url: `data:image/png;base64,${imageB64}` }] }] },
	});
}
// Discovered ids with no seed row: NeoPi's mapped defaults decide the wire.
scenarios.push({ ...plain("meta", "muse-spark-1.4", "xhigh"), name: "meta-lineage-muse-spark-1.4-xhigh" });
scenarios.push({ ...plain("meta", "muse-spark-1.4", "max"), name: "meta-lineage-muse-spark-1.4-max" });

const fixtures: any[] = [];
const lineage = (provider: string, id: string) => {
	const base = seed(provider, "muse-spark-1.3");
	const { thinking: _ladder, ...rest } = base;
	return { ...rest, id, name: `Muse Spark 1.4` };
};
try {
	for (const scenario of scenarios) {
		const spec = scenario.model === "muse-spark-1.4" ? lineage(scenario.provider, scenario.model) : seed(scenario.provider, scenario.model);
		const model = buildModel(spec);
		captured = [];
		let requestedURL = "";
		const redirectFetch = (async (input: any, init: any) => {
			requestedURL = input instanceof Request ? input.url : String(input);
			const target = new URL(requestedURL);
			const headers = new Headers(init?.headers);
			headers.set("host", target.host);
			return fetch(local + target.pathname + target.search, { ...init, headers });
		}) as typeof fetch;
		const context = { messages: scenario.messages, ...(scenario.tools ? { tools: scenario.tools } : {}), ...(scenario.systemPrompt ? { systemPrompt: scenario.systemPrompt } : {}) };
		// The provider transport runs first, as NeoPi's agent does: muse-code
		// unwraps the minted key and adds x-api-version.
		const definition = getProviderDefinition(scenario.provider);
		const prepared = definition?.prepareRequest
			? definition.prepareRequest(model, { apiKey: credentials[scenario.provider] })
			: { model, options: { apiKey: credentials[scenario.provider] } };
		// NeoPi's agent clamps the requested level to the model's ladder before the
		// provider sees it.
		const options = { ...scenario.options };
		if (options.reasoning) options.reasoning = clampThinkingLevelForModel(model, options.reasoning);
		const result = await streamOpenAIResponses(prepared.model, context as any, { ...prepared.options, sessionId, ...options, fetch: redirectFetch }).result();
		if (result.stopReason !== "stop") throw new Error(`${scenario.name}: ${result.errorMessage}`);
		fixtures.push({ name: scenario.name, requestedURL, scenario, requests: captured });
	}
	console.log(JSON.stringify({ source: "NeoPi OWN streamOpenAIResponses (meta, muse-code, sakana, abliteration)", userAgent: USER_AGENT, credentials, sessionId, response: sse, fixtures }, null, 2));
} finally {
	server.stop(true);
}
