// Runs NeoPi's OWN Nous Portal extension (testdata/neopi-extension/nous-portal,
// a verbatim copy of the profile extension NeoPi loads) through its registered
// provider hooks: oauth.login (device flow and pasted key), oauth.refreshToken
// and fetchDynamicModels. Every request goes to a loopback server only; the
// extension's https://portal.nousresearch.com and inference-api URLs are
// recorded as the logical target (x-capture-url) while the wire headers are the
// ones Bun's fetch really sent. Timers run on a fake clock so polling delays
// are recorded rather than waited out.
//
// The discovery rows are also run through NeoPi's own catalog build
// (pi-catalog buildModel) so the Go port is checked against resolved models.
//
//   NEOPI_SOURCE_MODULES=<...>/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_nous_auth.ts > neopi_nous_auth_capture.json
import { join } from "node:path";
import { ensureScratchHome } from "../../marineris/testdata/scratch_home.ts";
await ensureScratchHome();

const sourceModules = process.env.NEOPI_SOURCE_MODULES;
if (!sourceModules) throw new Error("NEOPI_SOURCE_MODULES is required");
const { buildModel } = await import(sourceModules + "/pi-catalog/src/build.ts");
const extensionDir = join(import.meta.dir, "neopi-extension", "nous-portal");
const originalFetch = globalThis.fetch;
const originalSetTimeout = globalThis.setTimeout;
const originalClearTimeout = globalThis.clearTimeout;

let clock = 1_700_000_000_000;
Date.now = () => clock;
let delays: number[] = [];
// The extension's own 15 s request timeout stays a real (never firing) timer;
// every other timer is a polling sleep that advances the fake clock.
(globalThis as any).setTimeout = (fn: () => void, ms?: number, ...rest: unknown[]) => {
	if (ms === 15_000) return originalSetTimeout(fn, ms, ...rest);
	delays.push(ms ?? 0);
	clock += ms ?? 0;
	return originalSetTimeout(fn, 0, ...rest);
};
(globalThis as any).clearTimeout = originalClearTimeout;

type Reply = { status?: number; body?: unknown; raw?: string };
let queue: Record<string, Reply[]> = {};
let requests: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const original = request.headers.get("x-capture-url")!;
		const headers = Object.fromEntries(request.headers);
		delete headers["x-capture-url"];
		requests.push({ method: request.method, url: original, headers, body: await request.text() });
		const target = new URL(original);
		const key = request.method + " " + target.origin + target.pathname;
		const replies = queue[key];
		const reply = replies?.length > 1 ? replies.shift()! : replies?.[0];
		if (!reply) return new Response("unexpected " + key, { status: 599 });
		if (reply.raw !== undefined) return new Response(reply.raw, { status: reply.status ?? 200 });
		return Response.json(reply.body ?? {}, { status: reply.status ?? 200 });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const target = new URL(input instanceof Request ? input.url : String(input));
		if (target.origin !== "https://portal.nousresearch.com" && target.origin !== "https://inference-api.nousresearch.com") {
			throw new Error("Unexpected Nous capture URL: " + target.href);
		}
		const headers = new Headers(options?.headers);
		headers.set("host", target.host);
		headers.set("x-capture-url", target.href);
		return originalFetch(`http://127.0.0.1:${server.port}${target.pathname}${target.search}`, { ...options, headers });
	},
	{ preconnect: originalFetch.preconnect },
);
(globalThis as any).fetch = captureFetch;

let provider: any;
const extension = (await import(join(extensionDir, "index.ts"))) as { default: (pi: any) => void; mapRow: (row: unknown) => unknown };
extension.default({ registerProvider: (id: string, config: any) => (provider = { id, config }) });
if (provider.id !== "nous-portal") throw new Error("unexpected provider id " + provider.id);

const PORTAL = "https://portal.nousresearch.com";
const INFERENCE = "https://inference-api.nousresearch.com";
const deviceCode = {
	device_code: "dev-code-0001",
	user_code: "ABCD-EFGH",
	verification_uri: PORTAL + "/device",
	verification_uri_complete: PORTAL + "/device?user_code=ABCD-EFGH",
	expires_in: 600,
	interval: 5,
};
const tokenOK = { access_token: "access-fake-1", refresh_token: "refresh-fake-1", expires_in: 3600 };
const post = (path: string) => "POST " + PORTAL + path;

// The login and discovery scenarios, each with scripted upstream replies.
const scenarios: any[] = [];
async function run(name: string, spec: { replies: Record<string, Reply[]>; prompts?: string[]; action: (callbacks: any) => Promise<unknown>; signal?: AbortSignal }) {
	queue = structuredClone(spec.replies);
	requests = [];
	delays = [];
	clock = 1_700_000_000_000;
	const events: any[] = [];
	const prompts = [...(spec.prompts ?? [])];
	const callbacks = {
		signal: spec.signal,
		onAuth: (info: unknown) => events.push({ onAuth: info }),
		onProgress: (message: string) => events.push({ onProgress: message }),
		onPrompt: async (prompt: unknown) => {
			events.push({ onPrompt: prompt });
			return prompts.shift() ?? "";
		},
	};
	let result: unknown, error: string | undefined;
	try {
		result = await spec.action(callbacks);
	} catch (failure) {
		error = String(failure instanceof Error ? failure.message : failure);
	}
	scenarios.push({ name, replies: spec.replies, events, result, error, delays, clockAdvanceMs: clock - 1_700_000_000_000, requests });
}

const login = (callbacks: any) => provider.config.oauth.login(callbacks);
const refresh = (refreshToken: string) => provider.config.oauth.refreshToken({ access: "old-access", refresh: refreshToken, expires: 0 });
const getApiKey = provider.config.oauth.getApiKey({ access: "tok", refresh: "r", expires: 1 });

try {
	await run("device-flow", {
		replies: {
			[post("/api/oauth/device/code")]: [{ body: deviceCode }],
			[post("/api/oauth/token")]: [
				{ status: 400, body: { error: "authorization_pending" } },
				{ status: 400, body: { error: "slow_down" } },
				{ status: 400, body: { error: "authorization_pending" } },
				{ body: tokenOK },
			],
		},
		action: login,
	});
	await run("device-flow-no-refresh-and-default-ttl", {
		replies: { [post("/api/oauth/device/code")]: [{ body: deviceCode }], [post("/api/oauth/token")]: [{ body: { access_token: "access-only" } }] },
		action: login,
	});
	await run("device-flow-short-ttl-skew", {
		replies: { [post("/api/oauth/device/code")]: [{ body: deviceCode }], [post("/api/oauth/token")]: [{ body: { ...tokenOK, expires_in: 600 } }] },
		action: login,
	});
	await run("device-flow-access-only-device-code-fallbacks", {
		replies: {
			[post("/api/oauth/device/code")]: [{ body: { device_code: "d2", user_code: "U2", verification_uri: PORTAL + "/device", interval: 40, expires_in: 12 } }],
			[post("/api/oauth/token")]: [{ status: 400, body: { error: "authorization_pending" } }],
		},
		action: login,
	});
	await run("device-flow-denied", {
		replies: { [post("/api/oauth/device/code")]: [{ body: deviceCode }], [post("/api/oauth/token")]: [{ status: 400, body: { error: "access_denied", error_description: "The user denied the request" } }] },
		action: login,
	});
	await run("device-flow-poll-message-fallback", {
		replies: { [post("/api/oauth/device/code")]: [{ body: deviceCode }], [post("/api/oauth/token")]: [{ status: 500, body: { message: "portal exploded" } }] },
		action: login,
	});
	await run("device-flow-poll-non-json", {
		replies: { [post("/api/oauth/device/code")]: [{ body: deviceCode }], [post("/api/oauth/token")]: [{ status: 502, raw: "<html>bad gateway</html>" }] },
		action: login,
	});
	await run("device-code-http-error", {
		replies: { [post("/api/oauth/device/code")]: [{ status: 403, body: { error: "forbidden", error_description: "blocked" } }] },
		action: login,
	});
	await run("device-code-foreign-verification-host", {
		replies: { [post("/api/oauth/device/code")]: [{ body: { ...deviceCode, verification_uri_complete: "https://evil.example/device" } }] },
		action: login,
	});
	await run("device-code-http-verification-scheme", {
		replies: { [post("/api/oauth/device/code")]: [{ body: { ...deviceCode, verification_uri_complete: "http://portal.nousresearch.com/device" } }] },
		action: login,
	});
	await run("device-code-incomplete", {
		replies: { [post("/api/oauth/device/code")]: [{ body: { verification_uri: PORTAL + "/device" } }] },
		action: login,
	});
	await run("device-code-expires-while-pending", {
		replies: {
			[post("/api/oauth/device/code")]: [{ body: { ...deviceCode, expires_in: 12, interval: 5 } }],
			[post("/api/oauth/token")]: [{ status: 400, body: { error: "authorization_pending" } }],
		},
		action: login,
	});
	await run("pasted-key", {
		replies: {},
		prompts: ["  sk-nous-pasted  "],
		action: login,
	});
	await run("refresh", {
		replies: { [post("/api/oauth/token")]: [{ body: { access_token: "access-2", refresh_token: "refresh-2", expires_in: 7200 } }] },
		action: () => refresh("refresh-1"),
	});
	await run("refresh-keeps-previous-refresh-token", {
		replies: { [post("/api/oauth/token")]: [{ body: { access_token: "access-3", expires_in: 100 } }] },
		action: () => refresh("refresh-1"),
	});
	await run("refresh-rejected", {
		replies: { [post("/api/oauth/token")]: [{ status: 401, body: { error: "invalid_grant", error_description: "refresh token revoked" } }] },
		action: () => refresh("refresh-1"),
	});
	await run("refresh-without-access-token", {
		replies: { [post("/api/oauth/token")]: [{ body: { refresh_token: "r" } }] },
		action: () => refresh("refresh-1"),
	});

	// Discovery. Rows exercise: batch aliases, non-text output, reasoning from
	// supported_parameters and from the row's own efforts, enrichment lookups,
	// the :free suffix, string-typed numbers and image modality.
	const rows = [
		{
			id: "qwen/qwen3.8-27b",
			name: "Qwen: Qwen3.8 27B",
			context_length: 262144,
			architecture: { modality: "text+image->text", output_modalities: ["text"] },
			pricing: { prompt: "0.00000036", completion: "0.00000256", input_cache_read: "0.00000003" },
			reasoning: { supported_efforts: ["low", "medium", "high"], default_effort: "medium" },
			supported_parameters: ["reasoning", "tools", "temperature"],
			top_provider: { max_completion_tokens: 65536 },
		},
		{ id: "deepseek/deepseek-v4-flash-0731", name: "DeepSeek V4 Flash", context_length: "1048576", architecture: { modality: "text->text", output_modalities: ["text"] }, pricing: { prompt: "0.0000001", completion: "0.0000004" }, supported_parameters: ["reasoning"] },
		{ id: "openai/gpt-5.6-luna:batch", name: "batch alias" },
		{ id: "google/gemini-3-pro-image", name: "image only", architecture: { output_modalities: ["image"] } },
		{ id: "~anthropic/claude-opus-latest", architecture: { modality: "text+image->text" }, supported_parameters: ["tools"] },
		{ id: "x-ai/grok-4.6:free", name: "Grok free", supported_parameters: ["reasoning", "tools"] },
		{ id: "meta/no-tools-model", supported_parameters: ["temperature"] },
		{ id: "deepseek/deepseek-v9-unseen", name: "DeepSeek unseen", supported_parameters: ["reasoning", "tools"] },
		{ id: "moonshotai/kimi-k9", name: "Kimi unseen", supported_parameters: ["reasoning"] },
		{ id: "anthropic/claude-unseen", name: "Claude unseen", architecture: { modality: "text+image->text" }, supported_parameters: ["reasoning", "tools"] },
		{ id: "plain/no-reasoning", name: "Plain", supported_parameters: ["tools"] },
		{ id: "vendor/unknown-model", name: "Unknown", reasoning: { supported_efforts: ["max", "minimal"], default_effort: "max" }, supported_parameters: ["tools"], top_provider: { max_completion_tokens: "4096" } },
		{ name: "no id" },
		"not-an-object",
	];
	const models = (key: string | undefined) => provider.config.fetchDynamicModels(key);
	await run("discovery-live", {
		replies: { ["GET " + INFERENCE + "/v1/models"]: [{ body: { data: rows } }] },
		action: () => models("sk-nous-key"),
	});
	const live = scenarios[scenarios.length - 1];
	live.rows = rows;
	live.resolved = (live.result as any[]).map(model => {
		const built = buildModel({ ...model, provider: "nous-portal", baseUrl: INFERENCE + "/v1" });
		return { id: built.id, name: built.name, api: built.api, reasoning: built.reasoning, input: built.input, contextWindow: built.contextWindow, maxTokens: built.maxTokens, thinking: built.thinking, compat: built.compat };
	});
	await run("discovery-http-error-falls-back-to-snapshot", {
		replies: { ["GET " + INFERENCE + "/v1/models"]: [{ status: 401, body: { error: "unauthorized" } }] },
		action: async () => {
			const list = (await models("sk-nous-key")) as any[];
			return { count: list.length, ids: list.map(model => model.id) };
		},
	});
	await run("discovery-invalid-catalog-falls-back-to-snapshot", {
		replies: { ["GET " + INFERENCE + "/v1/models"]: [{ body: { data: "nope" } }] },
		action: async () => ((await models("sk-nous-key")) as any[]).length,
	});
	await run("discovery-without-key-is-snapshot", {
		replies: {},
		action: async () => {
			const list = (await models(undefined)) as any[];
			return { count: list.length, first: list[0], ids: list.map(model => model.id) };
		},
	});
} finally {
	server.stop(true);
}
console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN nous-portal extension: oauth.login/refreshToken/getApiKey and fetchDynamicModels",
			provider: { id: provider.id, baseUrl: provider.config.baseUrl, api: provider.config.api, oauthName: provider.config.oauth.name, getApiKey },
			scenarios,
		},
		null,
		2,
	),
);
