// Runs NeoPi's OWN Antigravity usage provider (antigravityUsageProvider.fetchUsage:
// retrieveUserQuotaSummary first, fetchAvailableModels as the legacy fallback)
// against scripted Cloud Code Assist responses served over loopback, with the
// original request URL, headers and body recorded, plus NeoPi's quota-counter
// ranking helpers over each resulting report. Reset times are fixed instants
// around the pinned clock so reports are reproducible.
process.env.PI_AI_ANTIGRAVITY_VERSION = "2.26.0";
process.env.PI_AI_ANTIGRAVITY_OS = "linux";
process.env.PI_AI_ANTIGRAVITY_ARCH = "amd64";
delete process.env.PI_AI_ANTIGRAVITY_CL;
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const usage = await import(root + "/pi-ai/src/usage/google-antigravity.ts");

const nativeFetch = globalThis.fetch;
const nativeNow = Date.now;
const now = 1_800_000_000_000;
const iso = (offsetMs: number) => new Date(now + offsetMs).toISOString();
const HOUR = 3_600_000;
const DAY = 24 * HOUR;

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

const ok = (value: unknown) => ({ status: 200, body: JSON.stringify(value) });
const groupedSummary = {
	groups: [
		{
			displayName: "Gemini models",
			buckets: [
				{ bucketId: "gemini-pro-5h", displayName: "Five Hour Limit Remaining", window: "WINDOW_5H", remainingFraction: 0.8, resetTime: iso(2 * HOUR) },
				{ bucketId: "gemini-pro-weekly", displayName: "Weekly Limit Remaining", window: "WINDOW_WEEKLY", remainingFraction: 0.55, resetTime: iso(3 * DAY) },
				{ bucketId: "gemini-argon-weekly", displayName: "Weekly Limit Remaining", window: "WINDOW_WEEKLY", remainingFraction: 0.95, resetTime: iso(4 * DAY) },
				{ bucketId: "gemini-hidden", displayName: "Hidden", window: "WINDOW_5H", remainingFraction: 0.1, disabled: true },
			],
		},
		{
			displayName: "Claude and GPT (third party)",
			buckets: [
				{ bucketId: "3p-5h", displayName: "5 hour limit", window: "WINDOW_5H", remainingFraction: 0.05, resetTime: iso(HOUR) },
				{ bucketId: "3p-weekly", displayName: "Weekly Limit Remaining", window: "WINDOW_WEEKLY", remainingFraction: 0, resetTime: iso(5 * DAY) },
			],
		},
		{
			displayName: "Other",
			buckets: [
				{ bucketId: "misc-credits", displayName: "Credits", window: "WINDOW_MONTHLY", remainingAmount: "250" },
				{ bucketId: "misc-empty", displayName: "Empty", remainingAmount: 0 },
				{ bucketId: "misc-bad", displayName: "Bad amount", remainingAmount: "abc" },
				{ displayName: "No ids" },
			],
		},
		{ displayName: "Gemini Ultra", buckets: [{ bucketId: "ultra-day", displayName: "Daily", window: "WINDOW_DAILY", remainingFraction: 1.5 }] },
	],
};
const flatSummary = {
	buckets: [
		{ bucketId: "gemini-flash-5h", displayName: "5h", window: "5h", remainingFraction: 0.25, resetTime: iso(30 * 60_000) },
		{ bucketId: "gemini-flash-week", displayName: "Weekly", window: "week", remainingFraction: 0.75, resetTime: iso(2 * DAY) },
		{ bucketId: "3p-weekly", displayName: "7 day", remainingFraction: 0.4, resetTime: iso(6 * DAY) },
		{ bucketId: "other", remainingFraction: -2 },
		{ bucketId: "stringy", window: "x", remainingFraction: "0.5" },
	],
};
const legacyModels = {
	models: {
		"gemini-3-pro": {
			displayName: "Gemini 3 Pro",
			modelProvider: "MODEL_PROVIDER_GOOGLE",
			dailyQuotaInfo: { remainingFraction: 0.5, resetTime: iso(5 * HOUR) },
			weeklyQuotaInfo: { remainingFraction: 0.9, resetTime: iso(3 * DAY) },
		},
		"gemini-3-flash": {
			apiProvider: "API_PROVIDER_GOOGLE_GEMINI",
			dailyQuotaInfos: [{ remainingFraction: 0.3, resetTime: iso(6 * HOUR) }],
			weeklyQuotaInfos: [{ resetTime: iso(2 * DAY) }],
		},
		"tab_flash_lite_preview": { modelProvider: "MODEL_PROVIDER_GOOGLE", quotaInfo: { remainingFraction: 1 } },
		"claude-sonnet-4-6": {
			apiProvider: "API_PROVIDER_ANTHROPIC_VERTEX",
			quotaInfos: [
				{ remainingFraction: 0.7, resetTime: iso(2 * DAY) },
				{ remainingFraction: 0.2, resetTime: iso(4 * HOUR) },
			],
		},
		"gpt-oss-120b": {
			modelProvider: "MODEL_PROVIDER_OPENAI",
			quotaInfoByTier: { "g1-pro-tier": { remainingFraction: 1 }, Free: [{ resetTime: iso(HOUR) }, null] },
		},
		"mystery-model": {
			quotaInfoByWindow: { WINDOW_5H: { remainingFraction: 0.6, resetTime: iso(HOUR) }, "weekly_limit": [{ remainingFraction: 0.35, resetTime: iso(6 * DAY) }] },
			quotaInfosByWindow: { "7 day": { remainingFraction: 0.45 }, "24h": { remainingFraction: 0.65, resetTime: "garbage" } },
			quotaInfo: [{ windowId: "WINDOW_UNKNOWN", windowLabel: "Custom", remainingFraction: 0.15, resetTime: iso(DAY / 2) }, { remainingFraction: "0.5", resetTime: "2027-01-01" }, { remainingFraction: 0.05, resetTime: "2027-01-15T12:00:00+02:00" }],
		},
		"empty-model": {},
		"null-quota": { quotaInfo: null },
	},
};
const legacyGoogleOnlyBare = { models: { "tab_jump_flash_lite_preview": { modelProvider: "MODEL_PROVIDER_GOOGLE", quotaInfo: { remainingFraction: 1 } }, "gemma-3": { modelProvider: "MODEL_PROVIDER_GOOGLE", quotaInfo: { resetTime: iso(HOUR) } } } };

const base = { provider: "google-antigravity", credential: { type: "oauth", accessToken: "ya29.access", projectId: "proj-1", email: "pilot@example.com", accountId: "acct-9", expiresAt: now + HOUR } };
const fixtures: any[] = [
	{ name: "grouped-buckets", params: base, responses: [ok(groupedSummary)] },
	{ name: "flat-buckets", params: base, responses: [ok(flatSummary)] },
	{ name: "empty-summary-falls-back-to-legacy", params: base, responses: [ok({ groups: [{ displayName: "x", buckets: [] }] }), ok(legacyModels)] },
	{ name: "summary-not-found-legacy", params: base, responses: [{ status: 404, body: "nope" }, ok(legacyModels)] },
	{ name: "legacy-bare-google", params: base, responses: [{ status: 400, body: "bad" }, ok(legacyGoogleOnlyBare)] },
	{ name: "legacy-empty-models", params: base, responses: [ok({}), ok({})] },
	{ name: "summary-transient-then-sandbox", params: base, responses: [{ status: 503, body: "later" }, ok(flatSummary)] },
	{ name: "summary-rate-limited-everywhere-legacy-works", params: base, responses: [{ status: 429, body: "slow" }, { status: 500, body: "boom" }, { status: 200, body: JSON.stringify(legacyModels) }] },
	{ name: "both-fail", params: base, responses: [{ status: 404, body: "a" }, { status: 500, body: "b" }, { status: 502, body: "c" }] },
	{ name: "base-url-override", params: { ...base, baseUrl: "http://127.0.0.1:1//" }, responses: [ok(flatSummary)], rewriteOrigin: true },
	{ name: "no-account-id", params: { ...base, credential: { ...base.credential, accountId: undefined, email: undefined } }, responses: [ok(flatSummary)] },
	{ name: "no-project", params: { ...base, credential: { ...base.credential, projectId: undefined } }, responses: [] },
	{ name: "expired-token", params: { ...base, credential: { ...base.credential, expiresAt: now } }, responses: [] },
	{ name: "no-token", params: { ...base, credential: { ...base.credential, accessToken: undefined } }, responses: [] },
	{ name: "summary-invalid-json-then-legacy", params: base, responses: [{ status: 200, body: "not json" }, ok(legacyModels)] },
	{ name: "summary-empty-body", params: base, responses: [{ status: 200, body: "" }] },
	{ name: "summary-null-body", params: base, responses: [{ status: 200, body: "null" }] },
	{ name: "summary-whitespace-body", params: base, responses: [{ status: 200, body: " " }] },
	{ name: "legacy-empty-body", params: base, responses: [{ status: 404, body: "nope" }, { status: 200, body: "" }] },
	{ name: "legacy-whitespace-body", params: base, responses: [{ status: 404, body: "nope" }, { status: 200, body: "\n" }] },
	{ name: "summary-array-body-falls-back", params: base, responses: [ok([]), ok({})] },
	{
		name: "legacy-falsy-quota-values",
		params: base,
		responses: [
			{ status: 404, body: "nope" },
			ok({
				models: {
					"gemini-3-pro": {
						modelProvider: "MODEL_PROVIDER_GOOGLE",
						quotaInfo: false,
						quotaInfos: 0,
						dailyQuotaInfo: "",
						dailyQuotaInfos: null,
						weeklyQuotaInfo: { remainingFraction: 0.4, resetTime: iso(3 * DAY) },
						quotaInfoByTier: { Free: false, Pro: 0, Ultra: { remainingFraction: 0.9, resetTime: iso(HOUR) } },
						quotaInfoByWindow: { daily: "", weekly: { remainingFraction: 0.6, resetTime: iso(2 * DAY) } },
					},
				},
			}),
		],
	},
];

const modelIds = ["claude-opus-4-6", "claude-sonnet-4-6", "gemini-3-pro", "gemini-3-flash", "gemma-3", "tab_flash_lite_preview", "tab_foo", "gpt-oss-120b", "openai/gpt-oss-120b", "gpt-5", "some-other-model", ""];
const results: any[] = [];
try {
	for (const fixture of fixtures) {
		script = [...fixture.responses];
		requests = [];
		Date.now = () => now;
		globalThis.fetch = captureFetch as typeof fetch;
		let report: any = null;
		let error: string | null = null;
		try {
			report = await usage.antigravityUsageProvider.fetchUsage(fixture.params, { fetch: captureFetch });
		} catch (cause: any) {
			error = String(cause?.message ?? cause);
		}
		globalThis.fetch = nativeFetch;
		Date.now = nativeNow;
		let ranking: any = null;
		if (report) {
			const { raw: _raw, ...withoutRaw } = report;
			report = withoutRaw;
			ranking = {
				models: modelIds.map(modelId => {
					const context = modelId ? { modelId } : undefined;
					return {
						modelId,
						counterKey: usage.getAntigravityCounterKeyForModel(modelId) ?? null,
						scoped: usage.scopeAntigravityLimitsForModel(report, context).map((limit: any) => limit.id),
						primary: usage.antigravityRankingStrategy.findWindowLimits(report, context)?.primary?.id ?? null,
						blockScope: usage.antigravityRankingStrategy.blockScope(context),
					};
				}),
				healable: usage.antigravityRankingStrategy.healableBlockScopes(report).map((entry: any) => ({ blockScope: entry.blockScope, limits: entry.limits.map((limit: any) => limit.id) })),
			};
		}
		results.push({ name: fixture.name, params: fixture.params, responses: fixture.responses, requests, report, ranking, error });
	}
} finally {
	globalThis.fetch = nativeFetch;
	Date.now = nativeNow;
	server.stop(true);
}
console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN antigravityUsageProvider.fetchUsage and antigravityRankingStrategy",
			now,
			env: { PI_AI_ANTIGRAVITY_VERSION: "2.26.0", PI_AI_ANTIGRAVITY_OS: "linux", PI_AI_ANTIGRAVITY_ARCH: "amd64" },
			windowDefaults: usage.antigravityRankingStrategy.windowDefaults,
			fixtures: results,
		},
		null,
		2,
	),
);
