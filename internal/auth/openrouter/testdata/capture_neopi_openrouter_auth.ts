// Executes NeoPi's OWN openrouter sign-in (the compiled oauth-code rule through
// DeclarativeOAuthCodeFlow, including its paste-key short circuit) against
// synthetic responses on a loopback server. No request can reach OpenRouter.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { DeclarativeOAuthCodeFlow, resolveCallbackOptions } = await import(root + "/pi-ai/src/registry/engine/oauth-code.ts");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const policy = rules.auth.providers.find((entry: any) => entry.id === "openrouter");
if (!policy) throw new Error("openrouter auth rule missing");

type Reply = { status?: number; body: unknown };
let spec: any;
let requests: any[];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const original = request.headers.get("x-capture-url")!;
		const headers = Object.fromEntries(request.headers);
		delete headers["x-capture-url"];
		requests.push({ method: request.method, url: original, headers, body: await request.text() });
		const path = new URL(original).pathname;
		let reply: Reply | undefined;
		if (path === "/api/v1/auth/keys") reply = spec.token ?? { body: { key: "sk-or-v1-fake", user_id: "user_fake" } };
		else if (path === "/api/v1/auth/key") reply = spec.validate ?? { body: { data: { label: "fake" } } };
		if (!reply) return new Response("unexpected " + path, { status: 599 });
		return Response.json(reply.body, { status: reply.status ?? 200 });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const target = new URL(input instanceof Request ? input.url : String(input));
		if (target.origin !== "https://openrouter.ai") throw new Error("Unexpected OpenRouter capture URL: " + target.href);
		const headers = new Headers(options?.headers);
		headers.set("host", target.host);
		headers.set("x-capture-url", target.href);
		return fetch(`http://127.0.0.1:${server.port}${target.pathname}${target.search}`, { ...options, headers, signal: undefined });
	},
	{ preconnect: fetch.preconnect },
);

const scenarios: any[] = [];
try {
	for (spec of [
		{ name: "exchange" },
		{ name: "code-with-state-fragment", code: "the-code#fragment-state" },
		{ name: "exchange-http-error", token: { status: 400, body: { error: { message: "invalid code" } } } },
		{ name: "exchange-missing-key", token: { body: { user_id: "user_fake" } } },
		{ name: "paste-key", code: "sk-or-v1-pasted" },
		{ name: "paste-key-rejected", code: "sk-or-v1-bad", validate: { status: 401, body: { error: { message: "No auth credentials found", code: 401 } } } },
	]) {
		requests = [];
		const controller = new AbortController();
		const ctrl = { fetch: captureFetch, signal: controller.signal, onProgress() {}, onAuth() {} } as any;
		const options = await resolveCallbackOptions(policy.login.callback, policy.id);
		const flow = new DeclarativeOAuthCodeFlow(ctrl, policy.login, policy, options);
		const state = "0123456789abcdef0123456789abcdef";
		// The redirect NeoPi's callback server would hand the flow (localhost on the rule's port).
		const redirectUri = options.redirectUri ?? `http://${options.callbackHostname ?? "localhost"}:${options.preferredPort}${options.callbackPath}`;
		let credential: unknown, error: string | undefined;
		const auth = await flow.generateAuthUrl(state, redirectUri);
		try {
			credential = await flow.exchangeToken(spec.code ?? "the-code", state, redirectUri);
		} catch (failure) {
			error = String(failure instanceof Error ? failure.message : failure);
		}
		scenarios.push({ ...spec, state, redirectUri, auth, credential, error, requests });
	}
	console.log(JSON.stringify({ source: "NeoPi OWN DeclarativeOAuthCodeFlow (openrouter)", scenarios }, null, 2));
} finally {
	server.stop(true);
}
