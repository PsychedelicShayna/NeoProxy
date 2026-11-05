// Executes NeoPi's OWN google-antigravity credential lifecycle: the compiled
// `oauth-code` rule through registry/engine's DeclarativeOAuthCodeFlow (authorize
// URL, token exchange, userinfo, the google-antigravity-project hook with
// loadCodeAssist / onboardUser) and the compiled refresh rule through
// createRefresh. Every fetch (the engine's and the hook's global one) is
// replaced by a wrapper that records the ORIGINAL url, headers and body and
// serves scripted responses over loopback; PKCE randomness and the clock are
// pinned so the Go port can replay each scenario byte for byte.
process.env.PI_AI_ANTIGRAVITY_VERSION = "2.26.0";
process.env.PI_AI_ANTIGRAVITY_OS = "linux";
process.env.PI_AI_ANTIGRAVITY_ARCH = "amd64";
delete process.env.PI_AI_ANTIGRAVITY_CL;
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { DeclarativeOAuthCodeFlow, resolveCallbackOptions } = await import(root + "/pi-ai/src/registry/engine/oauth-code.ts");
const { createRefresh } = await import(root + "/pi-ai/src/registry/engine/refresh.ts");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const policy = rules.auth.providers.find((entry: any) => entry.id === "google-antigravity");
if (!policy) throw new Error("google-antigravity auth rule missing");

const nativeFetch = globalThis.fetch;
const nativeRandom = crypto.getRandomValues.bind(crypto);
const nativeNow = Date.now;
const now = 1_800_000_000_000;

let script: any[] = [];
let requests: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const headers = Object.fromEntries(request.headers);
		const origin = headers["x-capture-origin"];
		delete headers["x-capture-origin"];
		delete headers.host;
		requests.push({ method: request.method, url: origin + url.pathname + url.search, headers, body: await request.text() });
		const next = script.shift() ?? { status: 599, body: "capture script exhausted" };
		return new Response(next.body, { status: next.status, headers: { "content-type": "application/json" } });
	},
});
const captureFetch = Object.assign(
	async (input: any, options: any = {}) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		const headers = new Headers(options.headers ?? {});
		headers.set("x-capture-origin", url.origin);
		return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, { ...options, headers });
	},
	{ preconnect: nativeFetch.preconnect },
);

const redirectUri = `http://${policy.login.callback.hostname}:${policy.login.callback.port}${policy.login.callback.path}`;
const state = "0123456789abcdef0123456789abcdef";
const jsonBody = (value: unknown) => JSON.stringify(value);
const token = (extra: any = {}) => ({ status: 200, body: jsonBody({ access_token: "ya29.access", refresh_token: "1//refresh", expires_in: 3599, token_type: "Bearer", ...extra }) });
const userinfo = { status: 200, body: jsonBody({ id: "1", email: "pilot@example.com", verified_email: true }) };
const loadExisting = { status: 200, body: jsonBody({ currentTier: { id: "standard-tier" }, paidTier: { id: "g1-pro-tier" }, cloudaicompanionProject: "proj-existing" }) };

const scenarios: any[] = [
	{ name: "existing-tier", mode: "login", responses: [token(), userinfo, loadExisting, loadExisting] },
	{
		name: "paid-tier-missing-second-call",
		mode: "login",
		responses: [
			token(),
			userinfo,
			{ status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, cloudaicompanionProject: "proj-a" }) },
			{ status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: { id: "free-tier" }, cloudaicompanionProject: "proj-b" }) },
			{ status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: null, cloudaicompanionProject: "proj-c" }) },
			{ status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: { id: "free-tier" }, cloudaicompanionProject: "proj-d" }) },
		],
	},
	{
		name: "onboard-polling",
		mode: "login",
		responses: [
			token(),
			userinfo,
			{ status: 200, body: jsonBody({ allowedTiers: [{ id: "free-tier", isDefault: true }] }) },
			{ status: 200, body: jsonBody({ name: "operations/op-1", done: false }) },
			{ status: 200, body: jsonBody({ name: "operations/op-1", done: false }) },
			{ status: 200, body: jsonBody({ name: "operations/op-1", done: true, response: { "@type": "type.googleapis.com/google.internal.cloud.code.v1internal.OnboardUserResponse", cloudaicompanionProject: "proj-onboarded" } }) },
			{ status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: { id: "free-tier" }, cloudaicompanionProject: "proj-onboarded" }) },
		],
	},
	{
		name: "onboard-done-immediately",
		mode: "login",
		responses: [
			token(),
			userinfo,
			{ status: 200, body: jsonBody({ allowedTiers: [{ id: "free-tier" }], cloudaicompanionProject: "proj-seed", paidTier: {} }) },
			{ status: 200, body: jsonBody({ done: true, response: { "@type": "x" } }) },
			{ status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: {}, cloudaicompanionProject: "proj-seed" }) },
		],
	},
	{
		name: "free-tier-ineligible",
		mode: "login",
		responses: [token(), userinfo, { status: 200, body: jsonBody({ allowedTiers: [{ id: "standard-tier" }], ineligibleTiers: [{ tierId: "free-tier", reasonMessage: "Not available in your region", validationUrl: "https://example.com/verify" }] }) }],
	},
	{
		name: "free-tier-ineligible-no-url",
		mode: "login",
		responses: [token(), userinfo, { status: 200, body: jsonBody({ ineligibleTiers: [{ tierId: "free-tier", reasonMessage: "Not available", validationUrl: "" }] }) }],
	},
	{
		name: "free-tier-ineligible-but-allowed",
		mode: "login",
		responses: [token(), userinfo, { status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: {}, allowedTiers: [{ id: "free-tier" }], ineligibleTiers: [{ tierId: "free-tier", reasonMessage: "ignored" }], cloudaicompanionProject: "proj-allowed" }) }, { status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: {}, cloudaicompanionProject: "proj-allowed" }) }],
	},
	{
		name: "validation-required",
		mode: "login",
		responses: [token(), userinfo, { status: 403, body: jsonBody({ error: { code: 403, message: "Verify your account", status: "PERMISSION_DENIED", details: [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "VALIDATION_REQUIRED", metadata: { validation_url: "https://accounts.google.com/verify?id=1" } }] } }) }],
	},
	{ name: "load-http-error", mode: "login", responses: [token(), userinfo, { status: 500, body: "backend down" }] },
	{ name: "load-malformed-tier", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ currentTier: "free-tier" }) }] },
	{ name: "load-malformed-allowed", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ allowedTiers: [{ id: 5 }], cloudaicompanionProject: {} }) }] },
	{
		name: "no-project",
		mode: "login",
		responses: [token(), userinfo, { status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: {} }) }, { status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: {} }) }],
	},
	{
		name: "onboard-operation-error",
		mode: "login",
		responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ name: "operations/op-2", done: true, error: { code: 7, message: "denied" } }) }],
	},
	{
		name: "onboard-operation-error-no-message",
		mode: "login",
		responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ done: true, error: { code: 3 } }) }],
	},
	{
		name: "onboard-done-without-response",
		mode: "login",
		responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ done: true }) }],
	},
	{
		name: "onboard-operation-without-name",
		mode: "login",
		responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ done: false }) }],
	},
	{
		name: "onboard-http-error",
		mode: "login",
		responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 429, body: "slow down" }],
	},
	{
		name: "onboard-poll-http-error",
		mode: "login",
		responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ name: "operations/op-3" }) }, { status: 503, body: "later" }],
	},
	{
		name: "onboard-timeout",
		mode: "login",
		// Each Date.now() read advances 12s, so the 30s onboarding deadline lapses at its fourth read.
		advanceMs: 12_000,
		responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ name: "operations/op-4", done: false }) }],
	},
	{ name: "no-refresh-token", mode: "login", responses: [token({ refresh_token: undefined }), userinfo] },
	{ name: "empty-refresh-token", mode: "login", responses: [token({ refresh_token: "" }), userinfo] },
	{ name: "userinfo-rejected", mode: "login", responses: [token(), { status: 401, body: "nope" }, loadExisting, loadExisting] },
	{ name: "userinfo-no-email", mode: "login", responses: [token(), { status: 200, body: jsonBody({ id: "1" }) }, loadExisting, loadExisting] },
	{ name: "code-with-state-fragment", mode: "login", code: "4/0Aabc#echoed-state", responses: [token(), userinfo, loadExisting, loadExisting] },
	{ name: "code-with-special-characters", mode: "login", code: "4/0A b+c&d=e~f*g", responses: [token(), userinfo, loadExisting, loadExisting] },
	{ name: "token-rejected", mode: "login", responses: [{ status: 400, body: jsonBody({ error: "invalid_grant", error_description: "Bad Request" }) }] },
	{ name: "token-rejected-long-body", mode: "login", responses: [{ status: 502, body: "x".repeat(700) }] },
	{ name: "token-missing-access", mode: "login", responses: [{ status: 200, body: jsonBody({ refresh_token: "r", expires_in: 3600 }) }] },
	{ name: "token-missing-access-text", mode: "login", responses: [{ status: 200, body: "not json" }] },
	{ name: "token-missing-expires", mode: "login", responses: [{ status: 200, body: jsonBody({ access_token: "a", refresh_token: "r" }) }] },
	{ name: "token-numeric-access", mode: "login", responses: [{ status: 200, body: jsonBody({ access_token: 12345, refresh_token: "r", expires_in: 100 }) }, userinfo, loadExisting, loadExisting] },
	{ name: "token-fractional-expiry", mode: "login", responses: [{ status: 200, body: jsonBody({ access_token: "a", refresh_token: "r", expires_in: 1.5 }) }, userinfo, loadExisting, loadExisting] },
	// Malformed payloads: NeoPi validates both Cloud Code Assist bodies with
	// arktype schemas and fails the sign-in with arktype's summary text.
	{ name: "load-null", mode: "login", responses: [token(), userinfo, { status: 200, body: "null" }] },
	{ name: "load-number", mode: "login", responses: [token(), userinfo, { status: 200, body: "5" }] },
	{ name: "load-boolean", mode: "login", responses: [token(), userinfo, { status: 200, body: "true" }] },
	{ name: "load-bad-json", mode: "login", responses: [token(), userinfo, { status: 200, body: "{bad" }] },
	{ name: "load-empty-body", mode: "login", responses: [token(), userinfo, { status: 200, body: "" }] },
	{ name: "load-whitespace-body", mode: "login", responses: [token(), userinfo, { status: 200, body: " " }] },
	{ name: "load-tier-number", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ currentTier: 5 }) }] },
	{ name: "load-tier-id-number", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ currentTier: { id: 1 } }) }] },
	{ name: "load-allowed-object", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ allowedTiers: {} }) }] },
	{ name: "load-allowed-element-string", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ allowedTiers: ["x"] }) }] },
	{ name: "load-ineligible-types", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ ineligibleTiers: [{ tierId: 1, reasonMessage: 2, validationUrl: 3 }] }) }] },
	{ name: "load-project-number", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ cloudaicompanionProject: 5 }) }] },
	// arktype accepts an array wherever a fully optional object is expected.
	{ name: "load-array-tier-accepted", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ currentTier: [], allowedTiers: [["nested"]], cloudaicompanionProject: "proj-array" }) }, { status: 200, body: jsonBody({ currentTier: [], paidTier: null, cloudaicompanionProject: "proj-array" }) }, { status: 200, body: jsonBody({ currentTier: [], paidTier: { id: "free-tier" }, cloudaicompanionProject: "proj-array" }) }] },
	{ name: "load-array-root-accepted", mode: "login", responses: [token(), userinfo, { status: 200, body: "[]" }, { status: 200, body: jsonBody({ done: true, response: { "@type": "t" } }) }, { status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: { id: "free-tier" }, cloudaicompanionProject: "proj-root-array" }) }] },
	{ name: "load-multiple-errors", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ currentTier: 1, allowedTiers: 2, cloudaicompanionProject: 3, paidTier: 4 }) }] },
	{ name: "load-null-tier-accepted", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({ currentTier: null, paidTier: null, cloudaicompanionProject: "proj-null" }) }, { status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: { id: "free-tier" }, cloudaicompanionProject: "proj-null" }) }, { status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: { id: "free-tier" }, cloudaicompanionProject: "proj-null" }) }] },
	{ name: "op-null", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: "null" }] },
	{ name: "op-done-string", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ done: "x" }) }] },
	{ name: "op-name-number", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ name: 5 }) }] },
	{ name: "op-error-string", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ error: "x" }) }] },
	{ name: "op-error-code-string", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ error: { code: "x" } }) }] },
	{ name: "op-response-missing-type", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ response: {} }) }] },
	{ name: "op-response-array", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ response: [] }) }] },
	{ name: "op-done-true-no-response", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ done: true }) }] },
	{ name: "op-error-object", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ done: true, error: { code: 3 } }) }] },
	{ name: "op-error-empty", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ done: true, error: {} }) }] },
	{ name: "op-array-accepted", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: "[]" }] },
	{ name: "op-complete-then-load", mode: "login", responses: [token(), userinfo, { status: 200, body: jsonBody({}) }, { status: 200, body: jsonBody({ done: true, response: { "@type": "t" } }) }, { status: 200, body: jsonBody({ currentTier: { id: "free-tier" }, paidTier: { id: "free-tier" }, cloudaicompanionProject: "proj-after" }) }] },
	{
		name: "refresh-rotated",
		mode: "refresh",
		stored: { access: "old", refresh: "1//old-refresh", expires: 1, projectId: "proj-stored", email: "pilot@example.com" },
		responses: [token({ refresh_token: "1//rotated" })],
	},
	{
		name: "refresh-not-rotated",
		mode: "refresh",
		stored: { access: "old", refresh: "1//old-refresh", expires: 1, projectId: "proj-stored" },
		responses: [token({ refresh_token: undefined })],
	},
	{
		name: "refresh-empty-rotation",
		mode: "refresh",
		stored: { access: "old", refresh: "1//old-refresh", expires: 1, projectId: "proj-stored" },
		responses: [token({ refresh_token: "" })],
	},
	{ name: "refresh-missing-project", mode: "refresh", stored: { access: "old", refresh: "1//old-refresh", expires: 1 }, responses: [] },
	{ name: "refresh-empty-project", mode: "refresh", stored: { access: "old", refresh: "1//old-refresh", expires: 1, projectId: "" }, responses: [] },
	{ name: "refresh-whitespace-project", mode: "refresh", stored: { access: "old", refresh: "1//old-refresh", expires: 1, projectId: " " }, responses: [token()] },
	{ name: "refresh-empty-refresh-token", mode: "refresh", stored: { access: "old", refresh: "", expires: 1, projectId: "proj-stored" }, responses: [{ status: 400, body: jsonBody({ error: "invalid_request" }) }] },
	{ name: "refresh-revoked", mode: "refresh", stored: { access: "old", refresh: "1//old-refresh", expires: 1, projectId: "proj-stored" }, responses: [{ status: 400, body: jsonBody({ error: "invalid_grant", error_description: "Token has been expired or revoked." }) }] },
	{ name: "refresh-server-error", mode: "refresh", stored: { access: "old", refresh: "1//old-refresh", expires: 1, projectId: "proj-stored" }, responses: [{ status: 503, body: "unavailable" }] },
	{ name: "refresh-missing-access", mode: "refresh", stored: { access: "old", refresh: "1//old-refresh", expires: 1, projectId: "proj-stored" }, responses: [{ status: 200, body: jsonBody({ expires_in: 3600 }) }] },
	{ name: "refresh-missing-expires", mode: "refresh", stored: { access: "old", refresh: "1//old-refresh", expires: 1, projectId: "proj-stored" }, responses: [{ status: 200, body: jsonBody({ access_token: "new" }) }] },
	{ name: "refresh-special-token", mode: "refresh", stored: { access: "old", refresh: "1//a+b/c=d&e~f*g h", expires: 1, projectId: "proj-stored" }, responses: [token()] },
];

const describe = (cause: any) => ({ message: String(cause?.message ?? cause), kind: cause?.kind ?? null, status: cause?.status ?? null, class: cause?.constructor?.name ?? null });
const results: any[] = [];
try {
	for (const scenario of scenarios) {
		script = [...scenario.responses];
		requests = [];
		let counter = 0;
		crypto.getRandomValues = (<T extends ArrayBufferView>(array: T) => {
			const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
			for (let i = 0; i < bytes.length; i++) bytes[i] = (counter++ * 7 + 3) & 0xff;
			return array;
		}) as typeof crypto.getRandomValues;
		let clockReads = 0;
		Date.now = scenario.advanceMs ? () => now + scenario.advanceMs * clockReads++ : () => now;
		globalThis.fetch = captureFetch as typeof fetch;
		let authorize: any = null;
		let credentials: any = null;
		let error: any = null;
		try {
			if (scenario.mode === "login") {
				const options = await resolveCallbackOptions(policy.login.callback, policy.id);
				const flow = new DeclarativeOAuthCodeFlow({ fetch: captureFetch } as any, policy.login, policy, options);
				authorize = await flow.generateAuthUrl(state, redirectUri);
				credentials = await flow.exchangeToken(scenario.code ?? "4/0Acode", state, redirectUri);
			} else {
				credentials = await createRefresh(policy)!(scenario.stored);
			}
		} catch (cause) {
			error = describe(cause);
		}
		globalThis.fetch = nativeFetch;
		crypto.getRandomValues = nativeRandom;
		Date.now = nativeNow;
		results.push({ name: scenario.name, mode: scenario.mode, code: scenario.code ?? null, stored: scenario.stored ?? null, advanceMs: scenario.advanceMs ?? 0, clockReads, responses: scenario.responses, authorize, requests, credentials, error });
	}
} finally {
	globalThis.fetch = nativeFetch;
	crypto.getRandomValues = nativeRandom;
	Date.now = nativeNow;
	server.stop(true);
}
console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN google-antigravity auth rule via DeclarativeOAuthCodeFlow + createRefresh",
			now,
			redirectUri,
			state,
			env: { PI_AI_ANTIGRAVITY_VERSION: "2.26.0", PI_AI_ANTIGRAVITY_OS: "linux", PI_AI_ANTIGRAVITY_ARCH: "amd64" },
			callback: policy.login.callback,
			scopes: policy.login.scopes,
			scenarios: results,
		},
		null,
		2,
	),
);
