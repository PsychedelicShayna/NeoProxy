// Runs NeoPi's OWN openai-completions provider (streamSimple, the agent entry)
// over models its Nous Portal and Marineris extensions register, against a
// loopback capture server only: every request is redirected to 127.0.0.1 and
// the keys are fakes, so nothing reaches a host. The models are built exactly
// as NeoPi builds extension-registered rows (pi-catalog buildModel with the
// extension's own mapping).
//
// Writes <provider>.fixtures.json.gz beside this file's neopi_nous/ directory:
// each captured request next to the downstream Chat Completions body NeoProxy
// receives for it, plus the discovery rows the models came from.
//
//   HOME=$(mktemp -d) NEOPI_SOURCE_MODULES=<...>/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_nous_npi.ts
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
if (!homedir().startsWith(tmpdir() + "/")) throw new Error("run with HOME set to a scratch directory under " + tmpdir());
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const outDir = join(import.meta.dir, "neopi_nous");
const authDir = join(import.meta.dir, "../../../auth");

const sessionId = "capture-session-nous-0001";
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
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody: await request.text() });
		return new Response(chatSSE, { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
type Turn =
	| { kind: "user"; text?: string; blocks?: any[] }
	| { kind: "call"; think?: boolean; text?: string }
	| { kind: "result"; blocks: any[]; isError?: boolean }
	| { kind: "assistant"; text: string };
const blockToWire = (b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image_url", image_url: { url: `data:image/png;base64,${b.source.data}` } });
const blockToNeoPi = (b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: b.source.data, mimeType: b.source.media_type });
const chatTurns = (turns: Turn[]) =>
	turns.map(turn => {
		if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks!.map(blockToWire) };
		if (turn.kind === "assistant") return { role: "assistant", content: turn.text };
		if (turn.kind === "call") {
			const message: any = { role: "assistant", content: turn.text ?? "" };
			if (turn.think !== false) message.reasoning_content = "Need lookup.";
			message.tool_calls = [{ id: "call_capture_1", type: "function", function: { name: "lookup", arguments: "{\"term\":\"a\"}" } }];
			return message;
		}
		return { role: "tool", tool_call_id: "call_capture_1", content: turn.blocks.some((b: any) => b.type !== "text") ? turn.blocks.map(blockToWire) : turn.blocks.map((b: any) => b.text).join("\n") };
	});
const neopiTurns = (model: any, turns: Turn[]) =>
	turns.map(turn => {
		if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks!.map(blockToNeoPi), timestamp: 0 };
		if (turn.kind === "assistant") return { role: "assistant", content: [{ type: "text", text: turn.text }], api: model.api, provider: model.provider, model: model.id, usage, stopReason: "stop", timestamp: 0 };
		if (turn.kind === "call")
			return {
				role: "assistant",
				content: [...(turn.think === false ? [] : [{ type: "thinking", thinking: "Need lookup.", thinkingSignature: "reasoning_content" }]), ...(turn.text ? [{ type: "text", text: turn.text }] : []), { type: "toolCall", id: "call_capture_1", name: "lookup", arguments: { term: "a" } }],
				api: model.api, provider: model.provider, model: model.id, usage, stopReason: "toolUse", timestamp: 0,
			};
		return { role: "toolResult", toolCallId: "call_capture_1", toolName: "lookup", content: turn.blocks.map(blockToNeoPi), isError: turn.isError ?? false, timestamp: 0 };
	});
const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const lookupTurns: Turn[] = [{ kind: "user", text: "Use lookup." }];
const text = (value: string) => ({ type: "text", text: value });
const image = { type: "image", source: { type: "base64", media_type: "image/png", data: png } };
const history = (blocks: any[], call: { think?: boolean; text?: string } = {}): Turn[] => [{ kind: "user", text: "Use lookup." }, { kind: "call", ...call }, { kind: "result", blocks }];
const multiTurn: Turn[] = [{ kind: "user", text: "Hi." }, { kind: "assistant", text: "Hello." }, { kind: "user", text: "Again." }];

const signatureOf = (model: any) => JSON.stringify({ compat: model.compat, reasoning: model.reasoning, thinking: model.thinking, input: model.input });
const representatives = (models: any[], limit: number) => {
	const seen = new Map<string, any>();
	for (const model of [...models].sort((a, b) => a.id.localeCompare(b.id))) if (!seen.has(signatureOf(model))) seen.set(signatureOf(model), model);
	return [...seen.values()].slice(0, limit);
};

async function capture(provider: string, apiKey: string, models: any[], rows: unknown) {
	const picked = representatives(models, 20);
	const scenarios: any[] = [];
	const add = (model: any, name: string, turns: Turn[], options: any, extra: { system?: string[]; tools?: any[] } = {}) => {
		const context: any = { ...(extra.system ? { systemPrompt: extra.system } : {}), messages: neopiTurns(model, turns), ...(extra.tools ? { tools: extra.tools } : {}) };
		const downstream: any = { model: model.id, messages: [...(extra.system ?? []).map(s => ({ role: "system", content: s })), ...chatTurns(turns)], stream: true };
		if (extra.tools) downstream.tools = extra.tools.map((t: any) => ({ type: "function", function: { name: t.name, description: t.description, parameters: t.parameters } }));
		if (options.maxTokens) downstream.max_tokens = options.maxTokens;
		if (options.temperature !== undefined) downstream.temperature = options.temperature;
		if (options.topP !== undefined) downstream.top_p = options.topP;
		if (options.disableReasoning) downstream.reasoning_effort = "none";
		else if (options.reasoning) downstream.reasoning_effort = options.reasoning;
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
		add(model, "sampling", hello, { temperature: 0.3, topP: 0.9, maxTokens: 300 });
		add(model, "system-tool", lookupTurns, { reasoning: first, maxTokens: 512, temperature: 0.2 }, { system: ["A source system."], tools: [lookup] });
		add(model, "tool-history", history([text("Found.")], { text: "Calling." }), { reasoning: first }, { tools: [lookup] });
		add(model, "tool-history-nothink", history([text("Found.")], { think: false }), { reasoning: first }, { tools: [lookup] });
		add(model, "tool-result-image", history([text("Shot."), image]), { reasoning: first }, { tools: [lookup] });
		add(model, "user-image", [{ kind: "user", blocks: [text("Describe."), image] }], { reasoning: first });
		add(model, "multi-turn", multiTurn, { reasoning: first });
	}
	const fixtures: any[] = [];
	const byId = new Map(models.map(model => [model.id, model]));
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
		const base = { name: scenario.name, model: scenario.model, options: scenario.options, downstream: scenario.downstream };
		try {
			const result = await streamSimple(model, scenario.context, { apiKey, sessionId, fetch: redirectFetch, ...scenario.options }).result();
			if (result.stopReason !== "stop") {
				fixtures.push({ ...base, error: String(result.errorMessage) });
				continue;
			}
		} catch (error) {
			fixtures.push({ ...base, error: String(error) });
			continue;
		}
		if (captured.length !== 1) throw new Error(`Capture ${provider}/${scenario.name} saw ${captured.length} requests`);
		fixtures.push({ ...base, requestedURL, ...captured[0] });
	}
	await Bun.write(join(outDir, `${provider}.fixtures.json.gz`), Bun.gzipSync(JSON.stringify({ source: `NeoPi OWN streamSimple over the ${provider} extension's models`, provider, apiKey, sessionId, rows, fixtures })));
	console.error(`${provider}: ${picked.length} models, ${fixtures.length} fixtures, ${fixtures.filter(f => f.error).length} errors`);
}

// Nous Portal: the extension's bundled snapshot, built as NeoPi builds it.
const nousDir = join(authDir, "nous/testdata/neopi-extension/nous-portal");
const snapshot = JSON.parse(await Bun.file(join(nousDir, "snapshot.json")).text()) as any[];
const nousModels = snapshot.map(row => buildModel({ ...row, provider: "nous-portal", baseUrl: "https://inference-api.nousresearch.com/v1" }));
await capture("nous-portal", "sk-nous-fixturekey0001", nousModels, undefined);

// Marineris: rows through the extension's own mapping.
const marinerisRows = [
	{ id: "grok-4.6-reasoning", name: "Grok 4.6 (reasoning)", provider: "xai", context_window: 256000, max_output_tokens: 64000, prompt_image_token_price: 20000 },
	{ id: "grok-4.6-non-reasoning", owned_by: "xai", context_length: 256000, max_tokens: 32000 },
	{ id: "grok-code-fast-2", owned_by: "xai", input_modalities: ["text", "image"] },
	{ id: "grok-3-mini-reasoning", owned_by: "xai" },
	{ id: "llama-local", owned_by: "local", context_window: 8192 },
];
const originalFetch = globalThis.fetch;
(globalThis as any).fetch = Object.assign(async () => Response.json({ data: marinerisRows }), { preconnect: originalFetch.preconnect });
let marinerisProvider: any;
const extension = (await import(join(authDir, "marineris/testdata/neopi-extension/marineris/index.ts"))) as { default: (pi: any) => void };
extension.default({ registerProvider: (_id: string, config: any) => (marinerisProvider = config) });
const specs = (await marinerisProvider.fetchDynamicModels("mrn_fixturekey0001")) as any[];
(globalThis as any).fetch = originalFetch;
const marinerisModels = specs.map(spec => buildModel({ ...spec, provider: "marineris", baseUrl: "http://192.168.50.50:8801/v1" }));
await capture("marineris", "mrn_fixturekey0001", marinerisModels.filter(model => model.api === "openai-completions"), marinerisRows);
server.stop(true);
