// Runs NeoPi's OWN gateway providers (OpenCode Go, OpenCode Zen, Vercel AI
// Gateway) through streamSimple against a loopback capture server only. Keys
// are fake; no request can reach a gateway. Rows come from NeoPi's own model
// discovery over the recorded live listings, so each model carries the API,
// compat and effort ladder NeoPi resolves for it.
//
// Each lane answers with a minimal stream of its own wire format (Anthropic
// Messages, Chat Completions, Responses or Gemini generateContent).
//
//   cd <neopi env> && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_gateway_npi.ts | gzip -9 > gateway_npi_capture.json.gz
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
// The recorded listings and NeoPi export live with the registry fixture.
const dataDir = `${import.meta.dir}/../../../registry/testdata`;
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const options = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");

const apiKey = "fixture-key-gateway";
const sessionId = "capture-session-gateway-0001";
const installId = "capture-install-gateway";
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };

// --- capture server ---------------------------------------------------------
const anthropicSSE = [
	{ type: "message_start", message: { id: "msg_capture", type: "message", role: "assistant", model: "m", content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 3, output_tokens: 0 } } },
	{ type: "content_block_start", index: 0, content_block: { type: "text", text: "" } },
	{ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "Captured." } },
	{ type: "content_block_stop", index: 0 },
	{ type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 1 } },
	{ type: "message_stop" },
].map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join("");
const chatSSE = [
	{ id: "chatcmpl-capture", object: "chat.completion.chunk", created: 1, model: "m", choices: [{ index: 0, delta: { role: "assistant", content: "Captured." }, finish_reason: null }] },
	{ id: "chatcmpl-capture", object: "chat.completion.chunk", created: 1, model: "m", choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 3, completion_tokens: 1, total_tokens: 4 } },
].map(chunk => `data: ${JSON.stringify(chunk)}\n\n`).join("") + "data: [DONE]\n\n";
const responsesSSE = [
	{ type: "response.created", response: { id: "resp_capture", object: "response", created_at: 1, model: "m", status: "in_progress", output: [] } },
	{ type: "response.output_item.added", output_index: 0, item: { type: "message", id: "msg_capture", status: "in_progress", role: "assistant", content: [] } },
	{ type: "response.output_text.delta", item_id: "msg_capture", output_index: 0, content_index: 0, delta: "Captured." },
	{ type: "response.output_text.done", item_id: "msg_capture", output_index: 0, content_index: 0, text: "Captured." },
	{ type: "response.output_item.done", output_index: 0, item: { type: "message", id: "msg_capture", status: "completed", role: "assistant", content: [{ type: "output_text", text: "Captured.", annotations: [] }] } },
	{ type: "response.completed", response: { id: "resp_capture", object: "response", created_at: 1, model: "m", status: "completed", output: [{ type: "message", id: "msg_capture", status: "completed", role: "assistant", content: [{ type: "output_text", text: "Captured.", annotations: [] }] }], usage: { input_tokens: 3, output_tokens: 1, total_tokens: 4 } } },
].map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join("");
const googleSSE = `data: ${JSON.stringify({ candidates: [{ content: { role: "model", parts: [{ text: "Captured." }] }, finishReason: "STOP" }], usageMetadata: { promptTokenCount: 3, candidatesTokenCount: 1, totalTokenCount: 4 } })}\r\n\r\n`;
const sseFor = (path: string) =>
	path.endsWith("/messages") ? anthropicSSE : path.includes(":streamGenerateContent") || path.includes(":generateContent") ? googleSSE : path.endsWith("/responses") ? responsesSSE : chatSSE;

let captured: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const rawBody = await request.text();
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody });
		return new Response(sseFor(url.pathname), { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;

// --- NeoPi's resolved rows --------------------------------------------------
const gunzip = async (name: string) => new TextDecoder().decode(Bun.gunzipSync(new Uint8Array(await Bun.file(`${dataDir}/${name}.gz`).arrayBuffer())));
const stencil = await gunzip("gateway_stencil.json");
const rows = new Map<string, any>();
for (const [provider, manager] of [
	["opencode-go", options.opencodeGoModelManagerOptions],
	["opencode-zen", options.opencodeZenModelManagerOptions],
	["vercel-ai-gateway", options.vercelAiGatewayModelManagerOptions],
] as any) {
	const listing = await gunzip(`gateway_${provider}_models_live.json`);
	const fakeFetch = (async (input: any) => {
		const url = String(input instanceof Request ? input.url : input);
		if (url === "https://catalog.stencil.so/models.json.zstd") return new Response(stencil);
		if (/\/v1\/models(\?|$)/.test(url)) return new Response(listing);
		return new Response("not found", { status: 404 });
	}) as typeof fetch;
	const opts = manager({ apiKey, fetch: fakeFetch });
	opts.cacheDbPath = `/tmp/gateway-capture-rows-${process.pid}.db`;
	const result = await resolveProviderModels(opts, "online");
	for (const model of result.models) rows.set(`${provider}/${model.id}`, model);
}

// --- scenarios --------------------------------------------------------------
type Turn = { kind: "user"; text?: string; blocks?: any[] } | { kind: "call" } | { kind: "result"; blocks: any[]; isError?: boolean };
const text = (value: string) => ({ type: "text", text: value });
const image = { type: "image", source: { type: "base64", media_type: "image/png", data: png } };

const neopiMessage = (turn: Turn) => {
	if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks!.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: b.source.data, mimeType: b.source.media_type })) };
	if (turn.kind === "call") return { role: "assistant", content: [{ type: "thinking", thinking: "Need lookup.", thinkingSignature: "" }, { type: "toolCall", id: "call_capture_1", name: "lookup", arguments: { term: "a" } }], usage, stopReason: "toolUse", timestamp: 0 };
	return { role: "toolResult", toolCallId: "call_capture_1", toolName: "lookup", content: turn.blocks.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: b.source.data, mimeType: b.source.media_type })), isError: turn.isError ?? false, timestamp: 0 };
};

const claudeBody = (model: any, turns: Turn[], options: any, extra: any) => {
	const body: any = {
		model: model.id,
		messages: turns.map(turn =>
			turn.kind === "user"
				? { role: "user", content: turn.text ?? turn.blocks }
				: turn.kind === "call"
					? { role: "assistant", content: [{ type: "thinking", thinking: "Need lookup.", signature: "" }, { type: "tool_use", id: "call_capture_1", name: "lookup", input: { term: "a" } }] }
					: { role: "user", content: [{ type: "tool_result", tool_use_id: "call_capture_1", content: turn.blocks, ...(turn.isError ? { is_error: true } : {}) }] },
		),
		stream: true,
	};
	if (extra.system) body.system = extra.system;
	if (extra.tools) body.tools = [{ name: lookup.name, description: lookup.description, input_schema: lookup.parameters }];
	if (options.maxTokens) body.max_tokens = options.maxTokens;
	if (options.temperature !== undefined) body.temperature = options.temperature;
	if (options.disableReasoning) body.thinking = { type: "disabled" };
	else if (options.reasoning) Object.assign(body, { thinking: { type: "adaptive" }, output_config: { effort: options.reasoning } });
	if (options.toolChoice === "any") body.tool_choice = { type: "any" };
	else if (options.toolChoice?.type === "tool") body.tool_choice = { type: "tool", name: options.toolChoice.name };
	return body;
};

const chatBody = (model: any, turns: Turn[], options: any, extra: any) => {
	const chatTurn = (turn: Turn) => {
		if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks!.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image_url", image_url: { url: `data:image/png;base64,${b.source.data}` } })) };
		if (turn.kind === "call") return { role: "assistant", content: "", reasoning_content: "Need lookup.", tool_calls: [{ id: "call_capture_1", type: "function", function: { name: "lookup", arguments: "{\"term\":\"a\"}" } }] };
		// A tool result carrying an image is content parts; text-only results are one string.
		const parts = turn.blocks.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image_url", image_url: { url: `data:image/png;base64,${b.source.data}` } }));
		return { role: "tool", tool_call_id: "call_capture_1", content: turn.blocks.some((b: any) => b.type !== "text") ? parts : turn.blocks.map((b: any) => b.text).join("\n") };
	};
	const body: any = { model: model.id, messages: [...(extra.system ? [{ role: "system", content: extra.system }] : []), ...turns.map(chatTurn)], stream: true };
	if (extra.tools) body.tools = [{ type: "function", function: { name: lookup.name, description: lookup.description, parameters: lookup.parameters } }];
	if (options.maxTokens) body.max_tokens = options.maxTokens;
	if (options.temperature !== undefined) body.temperature = options.temperature;
	if (options.disableReasoning) body.reasoning_effort = "none";
	else if (options.reasoning) body.reasoning_effort = options.reasoning;
	if (options.toolChoice === "any") body.tool_choice = "required";
	else if (options.toolChoice?.type === "tool") body.tool_choice = { type: "function", function: { name: options.toolChoice.name } };
	return body;
};

const responsesBody = (model: any, turns: Turn[], options: any, extra: any) => {
	const items: any[] = [];

	for (const turn of turns) {
		if (turn.kind === "user") items.push({ role: "user", content: turn.text !== undefined ? [{ type: "input_text", text: turn.text }] : turn.blocks!.map((b: any) => (b.type === "text" ? { type: "input_text", text: b.text } : { type: "input_image", detail: "auto", image_url: `data:image/png;base64,${b.source.data}` })) });
		else if (turn.kind === "call") items.push({ type: "function_call", id: "fc_capture_1", call_id: "call_capture_1", name: "lookup", arguments: "{\"term\":\"a\"}" });
		else items.push({ type: "function_call_output", call_id: "call_capture_1", output: turn.blocks.some((b: any) => b.type !== "text") ? turn.blocks.map((b: any) => (b.type === "text" ? { type: "input_text", text: b.text } : { type: "input_image", detail: "auto", image_url: `data:image/png;base64,${b.source.data}` })) : turn.blocks.map((b: any) => b.text).join("\n") });
	}
	// A client names its system prompt in `instructions`; user turns are easy-input messages.
	const body: any = { model: model.id, input: items, ...(extra.system ? { instructions: extra.system } : {}) };
	if (extra.tools) body.tools = [{ type: "function", name: lookup.name, description: lookup.description, parameters: lookup.parameters }];
	if (options.maxTokens) body.max_output_tokens = options.maxTokens;
	if (options.temperature !== undefined) body.temperature = options.temperature;
	if (options.disableReasoning) body.reasoning = { effort: "none" };
	else if (options.reasoning) body.reasoning = { effort: options.reasoning };
	if (options.toolChoice === "any") body.tool_choice = "required";
	else if (options.toolChoice?.type === "tool") body.tool_choice = { type: "function", name: options.toolChoice.name };
	return body;
};

const downstreamFor = (model: any, turns: Turn[], options: any, extra: any) =>
	model.api === "anthropic-messages"
		? claudeBody(model, turns, options, extra)
		: model.api === "openai-responses"
			? responsesBody(model, turns, options, extra)
			: chatBody(model, turns, options, extra);

const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const lookupTurns: Turn[] = [{ kind: "user", text: "Use lookup." }];
const history = (blocks: any[], isError = false): Turn[] => [{ kind: "user", text: "Use lookup." }, { kind: "call" }, { kind: "result", blocks, isError }];

const plans: any[] = [
	{ row: "opencode-go/glm-5.3", effort: true },
	{ row: "opencode-go/deepseek-v4.1-flash", effort: true },
	{ row: "opencode-go/claude-haiku-5-5", effort: true },
	{ row: "opencode-go/qwen3.8-flash", effort: true },
	{ row: "opencode-go/gpt-6-luna", effort: true },
	{ row: "opencode-go/gemini-4-argon", effort: true },
	{ row: "opencode-zen/claude-opus-5", effort: true },
	{ row: "opencode-zen/claude-opus-4-5", effort: true },
	{ row: "opencode-zen/minimax-m3", effort: true },
	{ row: "opencode-zen/gpt-5.2", effort: true },
	{ row: "opencode-zen/gemini-3.5-flash", effort: true },
	{ row: "vercel-ai-gateway/anthropic/claude-opus-5", effort: true },
	{ row: "vercel-ai-gateway/deepseek/deepseek-v4-pro", effort: true },
	{ row: "vercel-ai-gateway/google/gemini-3-flash", effort: true },
];

const scenarios: any[] = [];
for (const plan of plans) {
	const model = rows.get(plan.row);
	if (!model) throw new Error("no resolved row " + plan.row);
	const efforts: string[] = model.thinking?.efforts ?? [];
	const add = (name: string, turns: Turn[], options: any, extra: any = {}) => {
		scenarios.push({ name: `${plan.row}/${name}`, row: plan.row, api: model.api, options, turns, extra, downstream: downstreamFor(model, turns, options, extra) });
	};
	add("plain", hello, {});
	if (efforts.length > 0) {
		add(`effort-${efforts[0]}`, hello, { reasoning: efforts[0] });
		add(`effort-${efforts[efforts.length - 1]}`, hello, { reasoning: efforts[efforts.length - 1] });
	}
	add("off", hello, { disableReasoning: true });
	add("off-sampling", hello, { disableReasoning: true, temperature: 0.3, maxTokens: 300 });
	add("system-tool", lookupTurns, { reasoning: efforts[0], maxTokens: 512, temperature: 0.2 }, { system: "A source system.", tools: true });
	add("tool-history", history([text("Found.")]), { reasoning: efforts[0] }, { tools: true });
	add("tool-result-blocks", history([text("Line one."), text("  "), text("Line two.")]), { reasoning: efforts[0] }, { tools: true });
	// Chat Completions history cannot express a tool error, so Google skips it.
	if (model.api !== "google-generative-ai") add("tool-result-empty-error", history([], true), { reasoning: efforts[0] }, { tools: true });
	add("forced-any", lookupTurns, { reasoning: efforts[0], toolChoice: "any" }, { tools: true });
	add("forced-named", lookupTurns, { reasoning: efforts[0], toolChoice: { type: "tool", name: "lookup" } }, { tools: true });
	add("user-image", [{ kind: "user", blocks: [text("Describe."), image] }], { reasoning: efforts[0] });
	add("tool-result-image", history([text("Shot."), image]), { reasoning: efforts[0] }, { tools: true });
}

const contextFor = (model: any, turns: Turn[], extra: any) => ({
	...(extra.system ? { systemPrompt: [extra.system] } : {}),
	messages: turns.map(turn => {
		if (turn.kind !== "call") return neopiMessage(turn);
		// A Chat Completions history carries no signature, so for Google it is a
		// foreign turn: NeoPi demotes its reasoning to text instead of replaying it.
		const foreign = model.api === "google-generative-ai";
		return { role: "assistant", content: [{ type: "thinking", thinking: "Need lookup.", thinkingSignature: "" }, { type: "toolCall", id: "call_capture_1", name: "lookup", arguments: { term: "a" } }], api: model.api, provider: foreign ? "neoproxy" : model.provider, model: foreign ? "neoproxy/" + model.id : model.id, usage, stopReason: "toolUse", timestamp: 0 };
	}),
	...(extra.tools ? { tools: [lookup] } : {}),
});

const fixtures: any[] = [];
try {
	for (const scenario of scenarios) {
		captured = [];
		const model = rows.get(scenario.row);
		let requestedURL = "";
		const redirectFetch = (async (input: any, init: any) => {
			requestedURL = input instanceof Request ? input.url : String(input);
			const u = new URL(requestedURL);
			if (u.origin !== new URL(model.baseUrl).origin) throw new Error("Unexpected non-capture request " + requestedURL);
			const headers = new Headers(init?.headers);
			headers.set("host", u.host);
			return fetch(local + u.pathname + u.search, { ...init, headers, tls: undefined });
		}) as typeof fetch;
		const result = await streamSimple(model, contextFor(model, scenario.turns, scenario.extra), { apiKey, sessionId, installId, fetch: redirectFetch, ...scenario.options }).result();
		if (result.stopReason !== "stop") throw new Error(`Capture ${scenario.name} failed: ${result.errorMessage}`);
		if (captured.length !== 1) throw new Error(`Capture ${scenario.name} saw ${captured.length} requests`);
		const request = captured[0];
		fixtures.push({ name: scenario.name, row: scenario.row, api: scenario.api, options: scenario.options, downstream: scenario.downstream, requestedURL, ...request, body: JSON.parse(request.rawBody) });
	}
	console.log(JSON.stringify({ source: "NeoPi OWN streamSimple over its resolved gateway rows", apiKey, sessionId, fixtures }));
} finally {
	server.stop(true);
}
