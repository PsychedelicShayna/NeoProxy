// Runs NeoPi's OWN openai-completions provider through streamSimple (the agent
// entry) for the providers named in NEOPI_PROVIDERS (comma list; default: the
// openai-keys lane) against a loopback capture server only: every request is
// redirected to 127.0.0.1 and the keys are fakes, so nothing reaches a host.
//
// Writes, per provider, into OUT_DIR (default: testdata/neopi_completions):
//   <provider>.fixtures.json.gz  the captured requests beside the downstream
//                                Chat Completions body NeoProxy receives for them
//   and, into CATALOG_DIR (default: internal/registry/models/neopi_completions):
//   <provider>.json.gz           NeoPi's bundled rows with resolved compat
//                                (deduplicated), plus the compat NeoPi resolves
//                                for a discovered id that is not bundled.
//
// Run from the NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { mkdirSync } from "node:fs";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const worktree = process.env.NEOPROXY_WORKTREE ?? "/mnt/alias/m2/agents/worktrees/neoproxy-openai-keys";
const outDir = process.env.OUT_DIR ?? join(worktree, "internal/runtime/executor/testdata/neopi_completions");
const catalogDir = process.env.CATALOG_DIR ?? join(worktree, "internal/registry/models/neopi_completions");
mkdirSync(outDir, { recursive: true });
mkdirSync(catalogDir, { recursive: true });

const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");

const providers = (process.env.NEOPI_PROVIDERS ?? "cerebras,deepseek,fireworks,groq,huggingface,mistral,moonshot,nanogpt,nvidia,together,alibaba-token-plan").split(",");
const maxRepresentatives = Number(process.env.MAX_MODELS_PER_PROVIDER ?? 24);

const sessionId = "capture-session-completions-0001";
const chatChunks = [
	{ id: "chatcmpl-capture", object: "chat.completion.chunk", created: 1, model: "capture", choices: [{ index: 0, delta: { role: "assistant", content: "Captured." }, finish_reason: null }] },
	{ id: "chatcmpl-capture", object: "chat.completion.chunk", created: 1, model: "capture", choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 3, completion_tokens: 1, total_tokens: 4 } },
];
const chatSSE = chatChunks.map(chunk => `data: ${JSON.stringify(chunk)}\n\n`).join("") + "data: [DONE]\n\n";
let captured: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const rawBody = await request.text();
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody });
		return new Response(chatSSE, { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const richTool = {
	name: "rich",
	description: "A tool whose schema uses constructs hosts treat differently.",
	parameters: {
		type: "object",
		properties: {
			mode: { const: "fast" },
			kind: { type: ["string", "null"], enum: ["a", "b", null] },
			items: { type: "array", items: { type: "string" }, minItems: 1, maxItems: 3 },
			label: { type: "string", pattern: "^[a-z]+$", minLength: 1 },
			opt: { type: "integer" },
		},
		required: ["mode", "items"],
		additionalProperties: false,
	},
};
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
const user = (content: unknown) => ({ role: "user", content, timestamp: 0 });

type Turn =
	| { kind: "user"; text?: string; blocks?: any[] }
	| { kind: "call"; think?: boolean; text?: string }
	| { kind: "result"; blocks: any[]; isError?: boolean }
	| { kind: "assistant"; text: string };

const assistantCall = (model: any, turn: { think?: boolean; text?: string }) => ({
	role: "assistant",
	content: [
		...(turn.think === false ? [] : [{ type: "thinking", thinking: "Need lookup.", thinkingSignature: "reasoning_content" }]),
		...(turn.text ? [{ type: "text", text: turn.text }] : []),
		{ type: "toolCall", id: "call_capture_1", name: "lookup", arguments: { term: "a" } },
	],
	api: model.api,
	provider: model.provider,
	model: model.id,
	usage,
	stopReason: "toolUse",
	timestamp: 0,
});
const toolResult = (content: unknown[], isError = false) => ({ role: "toolResult", toolCallId: "call_capture_1", toolName: "lookup", content, isError, timestamp: 0 });

const chatTurns = (turns: Turn[], withThinking: boolean) =>
	turns.map(turn => {
		if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks!.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image_url", image_url: { url: `data:image/png;base64,${b.source.data}` } })) };
		if (turn.kind === "assistant") return { role: "assistant", content: turn.text };
		if (turn.kind === "call") {
			const message: any = { role: "assistant", content: turn.text ?? "" };
			if (turn.think !== false && withThinking) message.reasoning_content = "Need lookup.";
			message.tool_calls = [{ id: "call_capture_1", type: "function", function: { name: "lookup", arguments: "{\"term\":\"a\"}" } }];
			return message;
		}
		const parts = turn.blocks.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image_url", image_url: { url: `data:image/png;base64,${b.source.data}` } }));
		return { role: "tool", tool_call_id: "call_capture_1", content: turn.blocks.some((b: any) => b.type !== "text") ? parts : turn.blocks.map((b: any) => b.text).join("\n") };
	});
const neopiTurns = (model: any, turns: Turn[]) =>
	turns.map(turn => {
		if (turn.kind === "user") return user(turn.text ?? turn.blocks!.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: b.source.data, mimeType: b.source.media_type })));
		if (turn.kind === "assistant") return { role: "assistant", content: [{ type: "text", text: turn.text }], api: model.api, provider: model.provider, model: model.id, usage, stopReason: "stop", timestamp: 0 };
		if (turn.kind === "call") return assistantCall(model, turn);
		return toolResult(turn.blocks.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: b.source.data, mimeType: b.source.media_type })), turn.isError);
	});

const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const lookupTurns: Turn[] = [{ kind: "user", text: "Use lookup." }];
const history = (blocks: any[], isError = false, call: { think?: boolean; text?: string } = {}): Turn[] => [{ kind: "user", text: "Use lookup." }, { kind: "call", ...call }, { kind: "result", blocks, isError }];
const text = (value: string) => ({ type: "text", text: value });
const image = { type: "image", source: { type: "base64", media_type: "image/png", data: png } };
const multiTurn: Turn[] = [{ kind: "user", text: "Hi." }, { kind: "assistant", text: "Hello." }, { kind: "user", text: "Again." }];

const signatureOf = (model: any) => JSON.stringify({ compat: model.compat, reasoning: model.reasoning, thinking: model.thinking, input: model.input });

function representatives(models: any[]): any[] {
	const seen = new Map<string, any>();
	for (const model of [...models].sort((a, b) => a.id.localeCompare(b.id))) {
		const key = signatureOf(model);
		if (!seen.has(key)) seen.set(key, model);
	}
	return [...seen.values()].slice(0, maxRepresentatives);
}

const apiKeyFor = (provider: string) => (provider === "alibaba-token-plan" ? "sk-sp-fixturekey00000001" : `fixture-key-${provider}-00000001`);

for (const provider of providers) {
	const bundled = getBundledModels(provider);
	const completions = bundled.filter((model: any) => model.api === "openai-completions");
	const skipped = bundled.length - completions.length;

	// Catalog export: deduplicated compats so large rosters stay small.
	const compats: any[] = [];
	const compatIndex = new Map<string, number>();
	const indexOf = (compat: any) => {
		const key = JSON.stringify(compat);
		if (!compatIndex.has(key)) {
			compatIndex.set(key, compats.length);
			compats.push(compat);
		}
		return compatIndex.get(key)!;
	};
	const baseUrl = completions[0]?.baseUrl;
	const unknownRow = buildModel({
		id: "zz-unknown-model-1",
		name: "zz-unknown-model-1",
		api: "openai-completions",
		provider,
		baseUrl,
		reasoning: false,
		input: ["text"],
		cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
		contextWindow: null,
		maxTokens: null,
	} as any);
	const rows = completions.map((model: any) => ({
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
		...(model.headers ? { headers: model.headers } : {}),
		completionsCompatIndex: indexOf(model.compat),
	}));
	const catalog = {
		source: `NeoPi OWN getBundledModels(${JSON.stringify(provider)}) with buildModel for a non-bundled id`,
		provider,
		baseUrl,
		skippedNonCompletions: skipped,
		compats,
		unknownCompatIndex: indexOf(unknownRow.compat),
		models: rows,
	};
	await Bun.write(join(catalogDir, `${provider}.json.gz`), Bun.gzipSync(JSON.stringify(catalog)));

	// Request captures.
	const apiKey = apiKeyFor(provider);
	const picked = representatives(completions);
	const scenarios: any[] = [];
	const add = (model: any, name: string, turns: Turn[], options: any, extra: { system?: string[]; tools?: any[] | null } = {}) => {
		const withThinking = true;
		const tools = extra.tools === undefined ? undefined : extra.tools;
		const context: any = {
			...(extra.system ? { systemPrompt: extra.system } : {}),
			messages: neopiTurns(model, turns),
			...(tools ? { tools } : tools === null ? { tools: undefined } : {}),
		};
		const downstream: any = {
			model: model.id,
			messages: [...(extra.system ?? []).map(s => ({ role: "system", content: s })), ...chatTurns(turns, withThinking)],
			stream: true,
		};
		if (tools) downstream.tools = tools.map((t: any) => ({ type: "function", function: { name: t.name, description: t.description, parameters: t.parameters } }));
		if (options.maxTokens) downstream.max_tokens = options.maxTokens;
		if (options.temperature !== undefined) downstream.temperature = options.temperature;
		if (options.topP !== undefined) downstream.top_p = options.topP;
		if (options.topK !== undefined) downstream.top_k = options.topK;
		if (options.minP !== undefined) downstream.min_p = options.minP;
		if (options.presencePenalty !== undefined) downstream.presence_penalty = options.presencePenalty;
		if (options.repetitionPenalty !== undefined) downstream.repetition_penalty = options.repetitionPenalty;
		if (options.disableReasoning) downstream.reasoning_effort = "none";
		else if (options.reasoning) downstream.reasoning_effort = options.reasoning;
		if (options.toolChoice === "any") downstream.tool_choice = "required";
		else if (options.toolChoice === "auto" || options.toolChoice === "none") downstream.tool_choice = options.toolChoice;
		else if (options.toolChoice?.type === "tool") downstream.tool_choice = { type: "function", function: { name: options.toolChoice.name } };
		scenarios.push({ name: `${model.id}/${name}`, model: model.id, context, options, downstream });
	};
	for (const model of picked) {
		const efforts: string[] = model.thinking?.efforts ?? [];
		const first = efforts[0];
		add(model, "plain", hello, {});
		if (model.reasoning && first) {
			add(model, `effort-${first}`, hello, { reasoning: first });
			const last = efforts[efforts.length - 1];
			if (last !== first) add(model, `effort-${last}`, hello, { reasoning: last });
		}
		add(model, "off", hello, { disableReasoning: true });
		add(model, "sampling", hello, { temperature: 0.3, topP: 0.9, topK: 40, minP: 0.05, presencePenalty: 0.1, repetitionPenalty: 1.1, maxTokens: 300 });
		add(model, "system-tool", lookupTurns, { reasoning: first, maxTokens: 512, temperature: 0.2 }, { system: ["A source system."], tools: [lookup] });
		add(model, "two-system", hello, { reasoning: first }, { system: ["First system.", "Second system."] });
		add(model, "rich-tool", lookupTurns, { reasoning: first }, { tools: [richTool, lookup] });
		add(model, "tool-history", history([text("Found.")], false, { text: "Calling." }), { reasoning: first }, { tools: [lookup] });
		add(model, "tool-history-nothink", history([text("Found.")], false, { think: false }), { reasoning: first }, { tools: [lookup] });
		add(model, "tool-history-no-tools", history([text("Found.")]), { reasoning: first }, { tools: null });
		add(model, "tool-result-blocks", history([text("Line one."), text("  "), text("Line two.")]), { reasoning: first }, { tools: [lookup] });
		add(model, "tool-result-empty-error", history([], true), { reasoning: first }, { tools: [lookup] });
		add(model, "tool-result-image", history([text("Shot."), image]), { reasoning: first }, { tools: [lookup] });
		add(model, "forced-any", lookupTurns, { reasoning: first, toolChoice: "any" }, { tools: [lookup] });
		add(model, "forced-named", lookupTurns, { reasoning: first, toolChoice: { type: "tool", name: "lookup" } }, { tools: [lookup] });
		add(model, "auto-choice", lookupTurns, { reasoning: first, toolChoice: "auto" }, { tools: [lookup] });
		add(model, "user-image", [{ kind: "user", blocks: [text("Describe."), image] }], { reasoning: first });
		add(model, "multi-turn", multiTurn, { reasoning: first });
	}

	const fixtures: any[] = [];
	const byId = new Map(completions.map((model: any) => [model.id, model]));
	for (const scenario of scenarios) {
		captured = [];
		const model = byId.get(scenario.model);
		let requestedURL = "";
		const redirectFetch = (async (input: any, init: any) => {
			requestedURL = input instanceof Request ? input.url : String(input);
			const u = new URL(requestedURL);
			const headers = new Headers(init?.headers);
			headers.set("host", u.host);
			return fetch(local + u.pathname + u.search, { ...init, headers, tls: undefined });
		}) as typeof fetch;
		let result: any;
		try {
			result = await streamSimple(model, scenario.context, { apiKey, sessionId, fetch: redirectFetch, ...scenario.options }).result();
		} catch (error) {
			fixtures.push({ name: scenario.name, model: scenario.model, options: scenario.options, downstream: scenario.downstream, error: String(error) });
			continue;
		}
		if (result.stopReason !== "stop") {
			fixtures.push({ name: scenario.name, model: scenario.model, options: scenario.options, downstream: scenario.downstream, error: String(result.errorMessage) });
			continue;
		}
		if (captured.length !== 1) throw new Error(`Capture ${provider}/${scenario.name} saw ${captured.length} requests`);
		fixtures.push({ name: scenario.name, model: scenario.model, options: scenario.options, downstream: scenario.downstream, requestedURL, ...captured[0] });
	}
	await Bun.write(
		join(outDir, `${provider}.fixtures.json.gz`),
		Bun.gzipSync(JSON.stringify({ source: `NeoPi OWN streamSimple over getBundledModels(${JSON.stringify(provider)})`, provider, apiKey, sessionId, fixtures })),
	);
	console.error(`${provider}: ${completions.length} rows, ${compats.length} compats, ${picked.length} models, ${fixtures.length} fixtures, ${fixtures.filter(f => f.error).length} errors`);
}
server.stop(true);
