// Runs NeoPi's OWN openrouter provider (api "openrouter" -> streamOpenAIResponses
// via streamSimple, the agent entry) against a loopback capture server only.
// The key is fake; no request can reach OpenRouter. NeoPi resolves compat by
// host, so every fixture uses the official base URL and the fetch redirect keeps
// traffic on the capture server. One representative model per compat signature
// runs each scenario; `downstream` is the Responses request a client sends
// NeoProxy for it and `options` is what NeoProxy maps onto NeoPi's stream options.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { USER_AGENT, APP_URL, PRODUCT_NAME } = await import(root + "/pi-utils/src/dirs.ts");

const sessionId = "capture-session-0001";
const apiKey = "sk-or-v1-capture";
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
const bundled = new Map(getBundledModels("openrouter").filter((model: any) => model.api === "openrouter").map((model: any) => [model.id, model]));
const row = (id: string) => {
	const model = bundled.get(id);
	if (!model) throw new Error(`missing openrouter model ${id}`);
	return model;
};

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const lookupWire = { type: "function", ...lookup };
const unionTool = { name: "pick", description: "Pick one.", parameters: { type: "object", anyOf: [{ properties: { a: { type: "string" } }, required: ["a"] }, { properties: { b: { type: "number" } }, required: ["b"] }] } };
const unionWire = { type: "function", ...unionTool };
const optionalTool = { name: "search", description: "Search.", parameters: { type: "object", properties: { query: { type: "string" }, limit: { type: "integer", minimum: 1 }, mode: { type: "string", enum: ["a", "b"] } }, required: ["query"] } };
const optionalWire = { type: "function", ...optionalTool };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
const assistant = (model: string, content: unknown[], items: unknown[]) => ({ role: "assistant", content, api: "openrouter", provider: "openrouter", model, usage, stopReason: "toolUse", timestamp: 1, providerPayload: { type: "openaiResponsesHistory", provider: "openrouter", dt: true, items } });
const userText = (text: string) => ({ role: "user", content: [{ type: "input_text", text }] });
const reasoningItem = { type: "reasoning", id: "rs_capture", summary: [], encrypted_content: "enc-capture" };
const callItem = { type: "function_call", id: "fc_capture", call_id: "call_1", name: "lookup", arguments: "{\"term\":\"a\"}" };

// One representative per resolved compat signature (see the profiles export).
const models = [
	"aion-labs/aion-2.0", "ai21/jamba-large-1.7", "amazon/nova-lite-v1", "anthropic/claude-3.5-sonnet", "anthropic/claude-haiku-4.5", "anthropic/claude-fable-5",
	"anthropic/claude-opus-5.5", "cohere/command-a-plus", "deepseek/deepseek-chat", "deepseek/deepseek-r1", "deepseek/deepseek-v4.1-flash", "google/gemini-2.0-flash-001",
	"google/gemini-2.5-flash", "meta/muse-spark-1.1", "minimax/minimax-m2", "moonshotai/kimi-k2", "moonshotai/kimi-k2.6", "moonshotai/kimi-k3", "openai/gpt-5",
	"openai/gpt-5.1-chat", "openai/gpt-5.6-luna", "openai/gpt-6-astra", "qwen/qwen3-30b-a3b", "qwen/qwen-2.5-72b-instruct", "x-ai/grok-3", "x-ai/grok-4",
];
const scenarioSpecs: any[] = [
	{ name: "plain-high", effort: "high", options: { temperature: 0.2, maxTokens: 99 }, system: "You are terse.", user: "Say hello.", down: { temperature: 0.2, max_output_tokens: 99 } },
	{ name: "no-effort", options: {}, user: "Say hello." },
	{ name: "low-effort", effort: "low", options: {}, user: "Quick." },
	{ name: "tools-auto", effort: "medium", options: { toolChoice: "auto" }, tools: [lookup, unionTool, optionalTool], toolsWire: [lookupWire, unionWire, optionalWire], user: "Use lookup.", down: { tool_choice: "auto" } },
	{ name: "tools-required", effort: "medium", options: { toolChoice: "any" }, tools: [lookup], toolsWire: [lookupWire], user: "Use lookup.", down: { tool_choice: "required" } },
	{ name: "tools-named", effort: "high", options: { toolChoice: { type: "tool", name: "lookup" } }, tools: [lookup, optionalTool], toolsWire: [lookupWire, optionalWire], user: "Use lookup.", down: { tool_choice: { type: "function", name: "lookup" } } },
	{ name: "tools-none", options: { toolChoice: "none" }, tools: [lookup], toolsWire: [lookupWire], user: "No tools.", down: { tool_choice: "none" } },
	{ name: "tool-history-replay", effort: "high", tools: [lookup], toolsWire: [lookupWire], history: true, options: {} },
	{ name: "image-input", effort: "medium", options: {}, image: true },
	{ name: "max-clamp", options: { maxTokens: 900000 }, user: "Long.", down: { max_output_tokens: 900000 } },
	{ name: "disable-reasoning", disable: true, options: {}, user: "Plain." },
	{ name: "tier-priority", effort: "high", options: { serviceTier: "priority" }, user: "Fast.", down: { service_tier: "priority" } },
	{ name: "tier-flex", options: { serviceTier: "flex" }, user: "Cheap.", down: { service_tier: "flex" } },
	{ name: "top-p", options: { topP: 0.9, temperature: 0.5 }, user: "Sample.", down: { top_p: 0.9, temperature: 0.5 } },
	{ name: "long-session", sessionOverride: "s".repeat(300), effort: "high", options: {}, user: "Say hello." },
	{ name: "verbosity", options: {}, user: "Say hello.", down: { text: { verbosity: "low" } } },
];
const scenarios: any[] = [];
for (const modelId of models) {
	const model = row(modelId);
	const efforts: string[] = model.thinking?.efforts ?? [];
	for (const spec of scenarioSpecs) {
		let effort = spec.effort;
		// The agent clamps to the model's ladder before streaming.
		if (effort && !efforts.includes(effort)) effort = efforts.includes("high") ? "high" : efforts.at(-1);
		if (!model.reasoning || !model.thinking) effort = undefined;
		const options: any = { ...spec.options };
		if (effort) options.reasoning = effort;
		if (spec.disable) options.disableReasoning = true;
		const down: any = { model: modelId, ...(spec.down ?? {}) };
		if (spec.system) down.instructions = spec.system;
		if (effort) down.reasoning = { effort };
		if (spec.disable) down.reasoning = { effort: "none" };
		if (spec.toolsWire) down.tools = spec.toolsWire;
		const messages: any[] = [];
		const input: any[] = [];
		if (spec.history) {
			messages.push({ role: "user", content: "Use lookup.", timestamp: 0 });
			messages.push(assistant(modelId, [{ type: "toolCall", id: "call_1|fc_capture", name: "lookup", arguments: { term: "a" } }], [reasoningItem, callItem]));
			messages.push({ role: "toolResult", toolCallId: "call_1|fc_capture", toolName: "lookup", content: [{ type: "text", text: "Found." }], isError: false, timestamp: 2 });
			input.push(userText("Use lookup."), reasoningItem, callItem, { type: "function_call_output", call_id: "call_1", output: "Found." });
		} else if (spec.image) {
			messages.push({ role: "user", content: [{ type: "text", text: "Describe." }, { type: "image", data: png, mimeType: "image/png" }], timestamp: 0 });
			input.push({ role: "user", content: [{ type: "input_text", text: "Describe." }, { type: "input_image", detail: "auto", image_url: `data:image/png;base64,${png}` }] });
		} else {
			messages.push({ role: "user", content: spec.user, timestamp: 0 });
			input.push(userText(spec.user));
		}
		down.input = input;
		scenarios.push({ name: `${modelId}/${spec.name}`, model: modelId, sessionless: spec.sessionless === true, sessionId: spec.sessionOverride ?? sessionId, options, tools: spec.tools, systemPrompt: spec.system ? [spec.system] : undefined, messages, downstream: down });
	}
}

const fixtures: any[] = [];
try {
	for (const scenario of scenarios) {
		const baseUrl = "https://openrouter.ai/api/v1";
		const model = { ...row(scenario.model), baseUrl };
		captured = [];
		const redirectFetch = (async (input: any, init: any) => {
			const target = new URL(input instanceof Request ? input.url : String(input));
			const headers = new Headers(init?.headers);
			headers.set("host", target.host);
			return fetch(local + target.pathname + target.search, { ...init, headers });
		}) as typeof fetch;
		const context = { messages: scenario.messages, ...(scenario.tools ? { tools: scenario.tools } : {}), ...(scenario.systemPrompt ? { systemPrompt: scenario.systemPrompt } : {}) };
		const options = { apiKey, ...(scenario.sessionless ? {} : { sessionId: scenario.sessionId }), ...scenario.options, fetch: redirectFetch };
		const result = await streamSimple(model, context as any, options as any).result();
		if (result.stopReason !== "stop") throw new Error(`${scenario.name}: ${result.errorMessage}`);
		if (captured.length !== 1) throw new Error(`${scenario.name}: ${captured.length} requests`);
		fixtures.push({ name: scenario.name, baseUrl, scenario: { model: scenario.model, sessionless: scenario.sessionless, sessionId: scenario.sessionId, downstream: scenario.downstream, options: scenario.options }, requests: captured });
	}
	console.log(JSON.stringify({ source: "NeoPi OWN streamSimple (openrouter, api openrouter -> openai-responses)", userAgent: USER_AGENT, appUrl: APP_URL, productName: PRODUCT_NAME, apiKey, sessionId, response: sse, fixtures }));
} finally {
	server.stop(true);
}
