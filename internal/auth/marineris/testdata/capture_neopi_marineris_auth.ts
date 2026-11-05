// Runs NeoPi's OWN Marineris extension (testdata/neopi-extension/marineris, a
// verbatim copy of the profile extension NeoPi loads) through its registered
// provider hooks: oauth.login (gateway URL and key prompts) and
// fetchDynamicModels, plus its config resolution. Every request goes to a
// loopback server; the gateway URL is recorded as the logical target
// (x-capture-url) and the wire headers are what Bun's fetch really sent. HOME
// is a scratch directory so the extension's config.json write is observable
// and never touches a real profile.
//
//   NEOPI_SOURCE_MODULES=<...>/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_marineris_auth.ts > neopi_marineris_auth_capture.json
import { readFileSync, existsSync, rmSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { ensureScratchHome } from "./scratch_home.ts";
await ensureScratchHome();

const sourceModules = process.env.NEOPI_SOURCE_MODULES;
if (!sourceModules) throw new Error("NEOPI_SOURCE_MODULES is required");
const { buildModel } = await import(sourceModules + "/pi-catalog/src/build.ts");
delete process.env.MARINERIS_BASE_URL;
delete process.env.MARINERIS_API_KEY;
const scratch = homedir();
const userConfig = join(scratch, ".omp", "agent", "extensions", "marineris", "config.json");

const originalFetch = globalThis.fetch;
type Reply = { status?: number; body?: unknown };
let queue: Reply[] = [];
let requests: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const original = request.headers.get("x-capture-url")!;
		const headers = Object.fromEntries(request.headers);
		delete headers["x-capture-url"];
		requests.push({ method: request.method, url: original, headers, body: await request.text() });
		const reply = queue.length > 1 ? queue.shift()! : queue[0];
		if (!reply) return new Response("unexpected", { status: 599 });
		return Response.json(reply.body ?? {}, { status: reply.status ?? 200 });
	},
});
(globalThis as any).fetch = Object.assign(
	async (input: any, options?: any) => {
		const target = new URL(input instanceof Request ? input.url : String(input));
		const headers = new Headers(options?.headers);
		headers.set("host", target.host);
		headers.set("x-capture-url", target.href);
		return originalFetch(`http://127.0.0.1:${server.port}${target.pathname}${target.search}`, { ...options, headers });
	},
	{ preconnect: originalFetch.preconnect },
);

const extensionDir = join(import.meta.dir, "neopi-extension", "marineris");

const rows = [
	{ id: "grok-4.6-reasoning", name: "Grok 4.6 (reasoning)", provider: "xai", context_window: 256000, max_output_tokens: 64000, prompt_text_token_price: 20000, completion_text_token_price: 60000, cached_prompt_text_token_price: 5000, prompt_image_token_price: 20000 },
	{ id: "grok-4.6-non-reasoning", owned_by: "xai", context_length: 256000, max_tokens: 32000, prompt_text_token_price: 20000, completion_text_token_price: 60000 },
	{ id: "grok-code-fast-2", owned_by: "xai", input_modalities: ["text", "image"] },
	{ id: "gpt-6.1-sol", name: "GPT-6.1 Sol", provider: "codex", context_window: 400000, prompt_text_token_price: 0, completion_text_token_price: 0 },
	{ id: "gpt-6.1-terra", provider: "codex" },
	{ id: "codex-mini", provider: "codex" },
	{ id: "o4-mini", name: "o4 mini" },
	{ id: "orbit-1", name: "Starts with o, so the extension treats it as Codex" },
	{ id: "llama-local", owned_by: "local", context_window: 8192 },
	{ id: "gateway-alias-codex", provider: "codex", name: "A gateway alias owned by codex" },
	{ id: "gateway-alias-owned", owned_by: "codex" },
	{ name: "no id" },
	"not-an-object",
];

const scenarios: any[] = [];
async function run(name: string, spec: { env?: Record<string, string>; prompts?: string[]; replies: Reply[]; keyOnDisk?: boolean; action: (provider: any, callbacks: any) => Promise<unknown> }) {
	rmSync(userConfig, { force: true });
	queue = structuredClone(spec.replies);
	requests = [];
	// Each scenario is a fresh load of the extension with its own environment.
	for (const key of ["MARINERIS_BASE_URL", "MARINERIS_API_KEY"]) delete process.env[key];
	Object.assign(process.env, spec.env ?? {});
	const registered: any[] = [];
	const extension = await import(join(extensionDir, "index.ts") + "?s=" + encodeURIComponent(name));
	extension.default({ registerProvider: (id: string, config: any) => registered.push({ id, config }) });
	const events: any[] = [];
	const prompts = [...(spec.prompts ?? [])];
	const callbacks = {
		onPrompt: async (prompt: unknown) => {
			events.push({ onPrompt: prompt });
			return prompts.shift() ?? "";
		},
	};
	let result: unknown, error: string | undefined;
	try {
		result = await spec.action(registered, callbacks);
	} catch (failure) {
		error = String(failure instanceof Error ? failure.message : failure);
	}
	const wrote = existsSync(userConfig) ? JSON.parse(readFileSync(userConfig, "utf8")) : undefined;
	const last = registered[registered.length - 1];
	scenarios.push({
		name,
		env: spec.env,
		replies: spec.replies,
		prompts: spec.prompts,
		registrations: registered.map(entry => ({ id: entry.id, baseUrl: entry.config.baseUrl, api: entry.config.api, apiKey: entry.config.apiKey, authHeader: entry.config.authHeader, oauthName: entry.config.oauth?.name })),
		events,
		result,
		error,
		configWritten: wrote,
		requests,
	});
	for (const key of ["MARINERIS_BASE_URL", "MARINERIS_API_KEY"]) delete process.env[key];
	void last;
}

const catalog = { data: rows };
const login = (registered: any[], callbacks: any) => registered[0].config.oauth.login(callbacks);
const models = (registered: any[], key: string | undefined) => registered[0].config.fetchDynamicModels(key);

try {
	await run("login-default-gateway", { replies: [{ body: catalog }], prompts: ["", "  mrn_secret_key  "], action: login });
	await run("login-custom-gateway-adds-scheme-and-v1", { replies: [{ body: catalog }], prompts: ["10.0.0.7:9000/", "mrn_key2"], action: login });
	await run("login-custom-gateway-https-with-v1", { replies: [{ body: catalog }], prompts: ["https://gw.example.invalid/v1///", "mrn_key3"], action: login });
	await run("login-empty-key", { replies: [{ body: catalog }], prompts: ["", "   "], action: login });
	await run("login-http-error", { replies: [{ status: 403, body: { error: "forbidden" } }], prompts: ["", "mrn_bad"], action: login });
	await run("login-invalid-catalog", { replies: [{ body: { data: "nope" } }], prompts: ["", "mrn_key"], action: login });
	await run("login-no-visible-models", { replies: [{ body: { data: [] } }], prompts: ["", "mrn_key"], action: login });
	await run("login-env-base-url-skips-config-write", { env: { MARINERIS_BASE_URL: "http://env-gw.invalid:1234" }, replies: [{ body: catalog }], prompts: ["", "mrn_env_key"], action: login });

	const discovery = await (async () => {
		await run("discovery-live", {
			replies: [{ body: catalog }],
			action: async (registered, _callbacks) => {
				const list = (await models(registered, "mrn_key")) as any[];
				// What NeoPi's catalog build resolves for each registered row.
				return list.map(model => {
					const built = buildModel({ ...model, provider: "marineris", baseUrl: registered[0].config.baseUrl });
					return { spec: model, built: { id: built.id, name: built.name, api: built.api, reasoning: built.reasoning, input: built.input, contextWindow: built.contextWindow, maxTokens: built.maxTokens, thinking: built.thinking, compat: built.compat } };
				});
			},
		});
	})();
	void discovery;
	await run("discovery-public-gateway", {
		env: { MARINERIS_BASE_URL: "https://gw.example.invalid/v1" },
		replies: [{ body: catalog }],
		action: async (registered, _callbacks) =>
			((await models(registered, "mrn_key")) as any[]).map(model => {
				const built = buildModel({ ...model, provider: "marineris", baseUrl: registered[0].config.baseUrl });
				return { spec: model, built: { id: built.id, name: built.name, api: built.api, reasoning: built.reasoning, input: built.input, contextWindow: built.contextWindow, maxTokens: built.maxTokens, thinking: built.thinking, compat: built.compat } };
			}),
	});
	await run("discovery-env-key-fallback", { env: { MARINERIS_API_KEY: "mrn_from_env" }, replies: [{ body: catalog }], action: async registered => ((await models(registered, undefined)) as any[]).length });
	await run("discovery-without-any-key", { replies: [{ body: catalog }], action: async registered => ((await models(registered, "")) as any[]).length });
	await run("discovery-http-error", { replies: [{ status: 500, body: {} }], action: async registered => models(registered, "mrn_key") });
} finally {
	server.stop(true);
}
console.log(JSON.stringify({ source: "NeoPi OWN marineris extension: oauth.login and fetchDynamicModels", rows, scenarios }, null, 2));
