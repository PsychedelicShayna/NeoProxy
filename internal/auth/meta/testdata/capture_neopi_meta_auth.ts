// Executes NeoPi's OWN muse-code device sign-in (compiled device-code rule +
// muse-code-key after-exchange hook), its subscription usage fetcher, and the
// api-key logins of meta/sakana/abliteration (models-endpoint validation)
// against synthetic responses on a loopback server. Nothing reaches Meta,
// Sakana or Abliteration.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getProviderDefinition } = await import(root + "/pi-ai/src/registry/registry.ts");
const { museCodeUsageProvider } = await import(root + "/pi-ai/src/usage/muse-code.ts");
const { USER_AGENT } = await import(root + "/pi-utils/src/dirs.ts");

type Reply = { status?: number; body: unknown };
let spec: Record<string, Reply[] | Reply>;
let requests: any[] = [];
const counters: Record<string, number> = {};
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const original = request.headers.get("x-capture-url")!;
		const headers = Object.fromEntries(request.headers);
		delete headers["x-capture-url"];
		requests.push({ method: request.method, url: original, headers, body: await request.text() });
		const path = new URL(original).pathname;
		const entry = spec[path];
		if (!entry) return new Response("unexpected " + path, { status: 599 });
		const replies = Array.isArray(entry) ? entry : [entry];
		const index = Math.min(counters[path] ?? 0, replies.length - 1);
		counters[path] = (counters[path] ?? 0) + 1;
		const reply = replies[index];
		return Response.json(reply.body, { status: reply.status ?? 200 });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const target = new URL(input instanceof Request ? input.url : String(input));
		const headers = new Headers(options?.headers);
		headers.set("host", target.host);
		headers.set("x-capture-url", target.href);
		return fetch(`http://127.0.0.1:${server.port}${target.pathname}${target.search}`, { ...options, headers, signal: options?.signal });
	},
	{ preconnect: fetch.preconnect },
);

const device = { device_code: "device-code-fake", user_code: "ABCD-EFGH", verification_uri: "https://www.meta.com/device", verification_uri_complete: "https://www.meta.com/device?code=ABCD-EFGH", interval: 1, expires_in: 600 };
const token = { access_token: "dca:account-token-fake", token_type: "bearer" };
const keyActive = { api_key: "LLM|minted-key", user_email: "Shay@Example.Invalid", user_id: "meta-user-1", is_subs_active: true, subs_tier_name: "Pro", subs_tier_id: "tier-pro", subs_usage: { window: { used_percent: 12.5, resets_at: "2026-11-04T20:00:00Z", window_duration_mins: 300 }, weekly: { used_percent: 40, resets_at: 1793836800 } } };

const out: any = { source: "NeoPi OWN muse-code login + usage, api-key logins", userAgent: USER_AGENT, device, token, keyActive, logins: [], usage: [], apiKeyLogins: [] };

async function museLogin(name: string, replies: Record<string, Reply[] | Reply>) {
	spec = replies;
	requests = [];
	for (const key of Object.keys(counters)) delete counters[key];
	const login = getProviderDefinition("muse-code")!.login!;
	const events: any[] = [];
	let result: any;
	let error: string | undefined;
	try {
		result = await login({ onAuth: (info: any) => events.push({ auth: info }), onProgress: (message: string) => events.push({ progress: message }), onPrompt: async () => "", fetch: captureFetch as any });
	} catch (cause: any) {
		error = String(cause?.message ?? cause);
	}
	out.logins.push({ name, replies, events, result, error, requests });
}
const paths = { device: "/oidc/device/authorization/", token: "/oidc/device/token/", key: "/muse-code/key" };
await museLogin("active", { [paths.device]: { body: device }, [paths.token]: [{ status: 400, body: { error: "authorization_pending" } }, { body: token }], [paths.key]: { body: keyActive } });
await museLogin("inactive", { [paths.device]: { body: device }, [paths.token]: { body: token }, [paths.key]: { body: { ...keyActive, is_subs_active: false } } });
await museLogin("payment-required", { [paths.device]: { body: device }, [paths.token]: { body: token }, [paths.key]: { body: { is_subs_active: true, require_payment: true, action_url: "https://www.meta.com/pay", user_email: "a@b.invalid" } } });
await museLogin("missing-key", { [paths.device]: { body: device }, [paths.token]: { body: token }, [paths.key]: { body: { user_email: "a@b.invalid", is_subs_active: true } } });
await museLogin("key-http-error", { [paths.device]: { body: device }, [paths.token]: { body: token }, [paths.key]: { status: 429, body: { error: "rate limited" } } });
await museLogin("denied", { [paths.device]: { body: device }, [paths.token]: { status: 400, body: { error: "access_denied" } } });
await museLogin("device-error", { [paths.device]: { status: 500, body: { error: "boom" } } });

const encoded = (apiKey = "LLM|minted-key") => JSON.stringify({ oauthAccessToken: "dca:account-token-fake", apiKey });
async function usage(name: string, reply: Reply, previous?: any) {
	spec = { [paths.key]: reply };
	requests = [];
	for (const key of Object.keys(counters)) delete counters[key];
	let report: any;
	let error: string | undefined;
	try {
		report = await museCodeUsageProvider.fetchUsage({ provider: "muse-code", credential: { type: "oauth", accessToken: encoded(), email: "cred@example.invalid", accountId: "cred-account" } } as any, { fetch: captureFetch as any, previousReport: previous });
	} catch (cause: any) {
		error = `${cause?.status ?? ""} ${cause?.message ?? cause}`.trim();
	}
	if (report) report = { ...report, fetchedAt: 0 };
	out.usage.push({ name, replies: spec, previous, report, error, requests });
	return report;
}
const first = await usage("windows", { body: keyActive });
await usage("idle-default", { body: { api_key: "LLM|minted-key", user_id: "meta-user-1", user_email: "shay@example.invalid", is_subs_active: true, subs_tier_name: "Pro" } });
await usage("idle-with-previous", { body: { api_key: "LLM|minted-key", user_id: "meta-user-1", is_subs_active: true } }, { provider: "muse-code", fetchedAt: 1, limits: [
	{ id: "300m", label: "5 Hours", scope: { provider: "muse-code" }, window: { id: "300m", label: "5 Hours", durationMs: 300 * 60_000 }, amount: { used: 10, limit: 100, usedFraction: 0.1, unit: "percent" }, status: "ok" },
	{ id: "1w", label: "Weekly", scope: { provider: "muse-code" }, window: { id: "1w", label: "Weekly", durationMs: 7 * 86400_000, resetsAt: 4102444800000 }, amount: { used: 40, limit: 100, usedFraction: 0.4, unit: "percent" }, status: "ok" },
] });
await usage("inactive", { body: { ...keyActive, is_subs_active: false } });
await usage("unauthorized", { status: 401, body: { error: "bad token" } });
await usage("server-error", { status: 500, body: { error: "oops" } });
await usage("payment-required", { body: { is_subs_active: true, require_payment: true } });

async function keyLogin(provider: string, name: string, reply: Reply, rawAnswer = " pasted-key ") {
	const modelsPath = "/v1/models";
	spec = { [modelsPath]: reply };
	requests = [];
	for (const key of Object.keys(counters)) delete counters[key];
	const events: any[] = [];
	let result: any;
	let error: string | undefined;
	try {
		result = await getProviderDefinition(provider)!.login!({ onAuth: (info: any) => events.push({ auth: info }), onProgress: (message: string) => events.push({ progress: message }), onPrompt: async (prompt: any) => (events.push({ prompt }), rawAnswer), fetch: captureFetch as any });
	} catch (cause: any) {
		error = `${cause?.status ?? ""} ${cause?.message ?? cause}`.trim();
	}
	out.apiKeyLogins.push({ provider, name, events, result, error, requests });
}
for (const provider of ["meta", "sakana", "abliteration"]) {
	await keyLogin(provider, "ok", { body: { data: [{ id: "x" }] } });
	await keyLogin(provider, "rejected", { status: 401, body: { error: { message: "Invalid API key" } } });
	await keyLogin(provider, "server-error", { status: 503, body: { error: "down" } });
	await keyLogin(provider, "empty", { body: {} }, "   ");
}
server.stop(true);
console.log(JSON.stringify(out, null, 2));
