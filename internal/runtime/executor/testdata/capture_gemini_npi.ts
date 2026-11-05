// Runs NeoPi's OWN google (Gemini API key) provider through streamSimple (the
// agent entry) against a loopback capture server only. The key is a fake; no
// request can reach Google. Every scenario is one NeoPi context plus the
// OpenAI Chat Completions request a client sends NeoProxy for it (the dialect
// NeoProxy translates every downstream format to before shaping).
//
//   cd <neopi env> && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_gemini_npi.ts > gemini_npi_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

const apiKey = "AIzaFixtureKey000000000000000000000000";
const sessionId = "capture-session-gemini-0001";
const okChunk = {
	candidates: [{ content: { role: "model", parts: [{ text: "Captured." }] }, finishReason: "STOP" }],
	usageMetadata: { promptTokenCount: 3, candidatesTokenCount: 1, totalTokenCount: 4 },
};
const okSSE = `data: ${JSON.stringify(okChunk)}\r\n\r\n`;
let captured: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const rawBody = await request.text();
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody });
		return new Response(okSSE, { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;
const bundled = new Map(getBundledModels("google").map((model: any) => [model.id, model]));
const row = (id: string) => {
	const model = bundled.get(id);
	if (!model) throw new Error("no bundled google row " + id);
	return model;
};

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const other = { name: "other", description: "", parameters: { type: "object", properties: { n: { type: ["integer", "null"] }, mode: { enum: ["a", "b"] } }, additionalProperties: false } };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
// A valid base64 thought signature (length % 4 == 0).
const signature = "EggKBgEMOdbHNA==";
const user = (content: unknown) => ({ role: "user", content, timestamp: 0 });
const assistant = (model: any, same: boolean, content: any[], stopReason = "toolUse") => ({
	role: "assistant",
	content,
	api: model.api,
	provider: same ? "google" : "neoproxy",
	model: same ? model.id : "neoproxy/" + model.id,
	usage,
	stopReason,
	timestamp: 0,
});
const toolResult = (id: string, name: string, content: unknown[], isError = false) => ({ role: "toolResult", toolCallId: id, toolName: name, content, isError, timestamp: 0 });

// A turn is one entry of the history; each renders to a NeoPi message and to an
// OpenAI Chat message.
type Block = { type: "text"; text: string } | { type: "image" };
type Turn =
	| { kind: "user"; text?: string; blocks?: Block[] }
	| { kind: "assistant"; text?: string }
	| { kind: "calls"; calls: { id: string; name: string; args: any; signed?: boolean }[]; thinking?: string; text?: string }
	| { kind: "result"; id: string; name: string; blocks: Block[]; isError?: boolean };
const imageBlock = { type: "image" } as const;
const neopiBlocks = (blocks: Block[]) => blocks.map(b => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: png, mimeType: "image/png" }));
const chatParts = (blocks: Block[]) => blocks.map(b => (b.type === "text" ? { type: "text", text: b.text } : { type: "image_url", image_url: { url: `data:image/png;base64,${png}` } }));
const neopiTurns = (model: any, turns: Turn[]) =>
	turns.map(turn => {
		switch (turn.kind) {
			case "user":
				return user(turn.text ?? neopiBlocks(turn.blocks!));
			case "assistant":
				return assistant(model, false, [{ type: "text", text: turn.text ?? "" }], "stop");
			case "calls": {
				// A call carrying a signature is the model's own turn; unsigned calls come from a foreign turn.
				const same = turn.calls.some(c => c.signed);
				const content: any[] = [];
				if (turn.thinking) content.push({ type: "thinking", thinking: turn.thinking, thinkingSignature: "" });
				if (turn.text) content.push({ type: "text", text: turn.text });
				for (const call of turn.calls) content.push({ type: "toolCall", id: call.id, name: call.name, arguments: call.args, ...(call.signed ? { thoughtSignature: signature } : {}) });
				return assistant(model, same, content);
			}
			case "result":
				return toolResult(turn.id, turn.name, neopiBlocks(turn.blocks), turn.isError);
		}
	});
const chatTurns = (turns: Turn[]) =>
	turns.map(turn => {
		switch (turn.kind) {
			case "user":
				return { role: "user", content: turn.text ?? chatParts(turn.blocks!) };
			case "assistant":
				return { role: "assistant", content: turn.text ?? "" };
			case "calls":
				return {
					role: "assistant",
					content: turn.text ?? "",
					...(turn.thinking ? { reasoning_content: turn.thinking } : {}),
					tool_calls: turn.calls.map(c => ({
						id: c.id,
						type: "function",
						function: { name: c.name, arguments: JSON.stringify(c.args) },
						...(c.signed ? { extra_content: { google: { thought_signature: signature } } } : {}),
					})),
				};
			case "result": {
				const hasImage = turn.blocks.some(b => b.type === "image");
				return { role: "tool", tool_call_id: turn.id, content: hasImage ? chatParts(turn.blocks) : turn.blocks.map(b => (b as any).text).join("\n") };
			}
		}
	});

const scenarios: any[] = [];
const add = (id: string, name: string, turns: Turn[], options: any, extra: { system?: string[]; tools?: any[] } = {}) => {
	const model = row(id);
	const context = { ...(extra.system ? { systemPrompt: extra.system } : {}), messages: neopiTurns(model, turns), ...(extra.tools ? { tools: extra.tools } : {}) };
	const downstream: any = {
		model: id,
		messages: [...(extra.system ?? []).map(text => ({ role: "system", content: text })), ...chatTurns(turns)],
		stream: true,
	};
	if (extra.tools) downstream.tools = extra.tools.map(t => ({ type: "function", function: { name: t.name, description: t.description, parameters: t.parameters } }));
	if (options.maxTokens) downstream.max_tokens = options.maxTokens;
	if (options.temperature !== undefined) downstream.temperature = options.temperature;
	if (options.topP !== undefined) downstream.top_p = options.topP;
	if (options.topK !== undefined) downstream.top_k = options.topK;
	if (options.presencePenalty !== undefined) downstream.presence_penalty = options.presencePenalty;
	if (options.serviceTier) downstream.service_tier = options.serviceTier;
	if (options.disableReasoning) downstream.reasoning_effort = "none";
	else if (options.reasoning) downstream.reasoning_effort = options.reasoning;
	if (options.toolChoice === "any") downstream.tool_choice = "required";
	else if (options.toolChoice === "auto" || options.toolChoice === "none") downstream.tool_choice = options.toolChoice;
	else if (options.toolChoice?.type === "tool") downstream.tool_choice = { type: "function", function: { name: options.toolChoice.name } };
	scenarios.push({ name: `${id}/${name}`, model: id, context, options, downstream });
};

const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const lookupTurns: Turn[] = [{ kind: "user", text: "Use lookup." }];
const call = (signed = false, thinking?: string, text?: string): Turn => ({ kind: "calls", calls: [{ id: "call_capture_1", name: "lookup", args: { term: "a" }, signed }], thinking, text });
const text = (value: string): Block => ({ type: "text", text: value });
const history = (blocks: Block[], isError = false, signed = false): Turn[] => [
	{ kind: "user", text: "Use lookup." },
	call(signed, "Need lookup."),
	{ kind: "result", id: "call_capture_1", name: "lookup", blocks, isError },
];

const ids = [
	"gemini-3.1-pro-preview",
	"gemini-3-flash-preview",
	"gemini-3.7-flash",
	"gemini-3.5-flash-lite",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	"gemini-flash-latest",
	"gemini-2.0-flash",
	"gemma-4-31b-it",
];
for (const id of ids) {
	const model = row(id);
	const efforts: string[] = model.thinking?.efforts ?? [];
	const first = efforts[0];
	const level = (value?: string) => (value ? { reasoning: value } : {});
	add(id, "plain", hello, {});
	for (const effort of efforts) add(id, `effort-${effort}`, hello, { reasoning: effort });
	add(id, "off", hello, { disableReasoning: true });
	add(id, "sampling", hello, { ...level(first), temperature: 0.3, topP: 0.9, topK: 40, presencePenalty: 0.2, maxTokens: 300 });
	add(id, "system", hello, { ...level(first) }, { system: ["A source system.", "  ", "Second system."] });
	add(id, "tools", lookupTurns, { ...level(first), maxTokens: 512 }, { system: ["A source system."], tools: [lookup, other] });
	add(id, "tool-history", history([text("Found.")]), level(first), { tools: [lookup] });
	add(id, "tool-history-signed", history([text("Found.")], false, true), level(first), { tools: [lookup] });
	add(id, "tool-result-blocks", history([text("Line one."), text("  "), text("Line two.")]), level(first), { tools: [lookup] });
	// A tool result's isError flag has no OpenAI Chat carrier, so error tool
	// results are covered by gemini_neopi_unit_test.go instead of a capture.
	add(id, "tool-result-empty", history([]), level(first), { tools: [lookup] });
	add(id, "forced-any", lookupTurns, { ...level(first), toolChoice: "any" }, { tools: [lookup] });
	add(id, "forced-named", lookupTurns, { ...level(first), toolChoice: { type: "tool", name: "lookup" } }, { tools: [lookup, other] });
	add(id, "choice-none", lookupTurns, { ...level(first), toolChoice: "none" }, { tools: [lookup] });
	add(id, "choice-auto", lookupTurns, { ...level(first), toolChoice: "auto" }, { tools: [lookup] });
	add(id, "user-image", [{ kind: "user", blocks: [text("Describe."), imageBlock] }], level(first));
	add(id, "tool-result-image", history([text("Shot."), imageBlock]), level(first), { tools: [lookup] });
	add(id, "tool-result-image-only", history([imageBlock]), level(first), { tools: [lookup] });
	add(id, "tier-priority", hello, { ...level(first), serviceTier: "priority" });
	add(id, "tier-flex", hello, { ...level(first), serviceTier: "flex" });
	add(id, "tier-auto", hello, { ...level(first), serviceTier: "auto" });
}
// Cross-cutting history shapes on one Gemini 3 and one Gemini 2.5 row.
for (const id of ["gemini-3.1-pro-preview", "gemini-2.5-flash"]) {
	const efforts: string[] = row(id).thinking?.efforts ?? [];
	const first = efforts[0];
	const two = (signedFirst: boolean, signedSecond: boolean): Turn => ({
		kind: "calls",
		calls: [
			{ id: "call_a", name: "lookup", args: { term: "a" }, signed: signedFirst },
			{ id: "call_b", name: "lookup", args: { term: "b" }, signed: signedSecond },
		],
	});
	const result = (id: string, value: string): Turn => ({ kind: "result", id, name: "lookup", blocks: [text(value)] });
	add(id, "parallel-ordered", [{ kind: "user", text: "Two." }, two(false, false), result("call_a", "A"), result("call_b", "B")], { reasoning: first }, { tools: [lookup] });
	add(id, "parallel-reordered", [{ kind: "user", text: "Two." }, two(false, false), result("call_b", "B"), result("call_a", "A")], { reasoning: first }, { tools: [lookup] });
	add(id, "parallel-signed-first", [{ kind: "user", text: "Two." }, two(true, false), result("call_a", "A"), result("call_b", "B")], { reasoning: first }, { tools: [lookup] });
	add(id, "parallel-image-results", [
		{ kind: "user", text: "Two." },
		two(false, false),
		{ kind: "result", id: "call_b", name: "lookup", blocks: [text("B"), imageBlock] },
		{ kind: "result", id: "call_a", name: "lookup", blocks: [text("A"), imageBlock] },
	], { reasoning: first }, { tools: [lookup] });
	add(id, "orphan-call", [{ kind: "user", text: "Use lookup." }, call(), { kind: "user", text: "Never mind." }], { reasoning: first }, { tools: [lookup] });
	// An orphan tool result becomes NeoPi's stale-tool-result note, but its tool
	// NAME exists only on the NeoPi message (an OpenAI Chat tool message carries
	// just tool_call_id), so that shape is covered by gemini_neopi_unit_test.go.
	add(id, "odd-call-id", [{ kind: "user", text: "Use lookup." }, { kind: "calls", calls: [{ id: "call/odd id|fc_1", name: "lookup", args: { term: "é\u2028😀" } }] }, { kind: "result", id: "call/odd id|fc_1", name: "lookup", blocks: [text("ok")] }], { reasoning: first }, { tools: [lookup] });
	add(id, "multi-turn", [
		{ kind: "user", text: "First." },
		{ kind: "assistant", text: "Answer one." },
		{ kind: "user", text: "Second." },
		call(false, "Think.", "Calling."),
		{ kind: "result", id: "call_capture_1", name: "lookup", blocks: [text("R")] },
		{ kind: "assistant", text: "Answer two." },
		{ kind: "user", text: "Third." },
	], { reasoning: first }, { tools: [lookup] });
	add(id, "empty-user-skipped", [{ kind: "user", text: "  " }, { kind: "user", text: "Real." }], { reasoning: first });
	add(id, "user-text-blocks", [{ kind: "user", blocks: [text("One."), text("  "), text("Two.")] }], { reasoning: first });
}

const fixtures: any[] = [];
try {
	for (const scenario of scenarios) {
		captured = [];
		const model = row(scenario.model);
		let requestedURL = "";
		const redirectFetch = (async (input: any, init: any) => {
			requestedURL = input instanceof Request ? input.url : String(input);
			const u = new URL(requestedURL);
			if (u.origin !== new URL(model.baseUrl).origin) throw new Error("Unexpected non-capture request " + requestedURL);
			const headers = new Headers(init?.headers);
			headers.set("host", u.host);
			return fetch(local + u.pathname + u.search, { ...init, headers, tls: undefined });
		}) as typeof fetch;
		const result = await streamSimple(model, scenario.context, { apiKey, sessionId, fetch: redirectFetch, ...scenario.options }).result();
		if (result.stopReason !== "stop") throw new Error(`Capture ${scenario.name} failed: ${result.errorMessage}`);
		if (captured.length !== 1) throw new Error(`Capture ${scenario.name} saw ${captured.length} requests`);
		const request = captured[0];
		fixtures.push({ name: scenario.name, model: scenario.model, options: scenario.options, downstream: scenario.downstream, requestedURL, ...request, body: JSON.parse(request.rawBody) });
	}
	console.log(JSON.stringify({ source: 'NeoPi OWN streamSimple over getBundledModels("google")', apiKey, sessionId, fixtures }, null, 2));
} finally {
	server.stop(true);
}
