// Records what NeoPi's OWN Antigravity roster discovery puts on the wire and
// what it makes of each answer: fetchAntigravityDiscoveryModels with a scripted
// fetch, over the real daily → sandbox endpoint fallback.
//
//   bun capture_antigravity_npi_discovery_wire.ts > antigravity_npi_discovery_wire.json
//
// Every scenario is one scripted answer per endpoint; the output lists the
// requests NeoPi sent (method, URL, the headers it sets, body) and the outcome
// (the collapsed roster, a rejected status, or null for a transient failure).
process.env.PI_AI_ANTIGRAVITY_VERSION = "2.26.0";
process.env.PI_AI_ANTIGRAVITY_OS = "linux";
process.env.PI_AI_ANTIGRAVITY_ARCH = "amd64";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { fetchAntigravityDiscoveryModels, ANTIGRAVITY_PRIMARY_ENDPOINT, ANTIGRAVITY_SANDBOX_ENDPOINT } = await import(
	root + "/pi-catalog/src/discovery/antigravity.ts"
);

const ROSTER = {
	models: {
		"gemini-3.1-pro-low": {
			displayName: "Gemini 3.1 Pro (Low)",
			supportsImages: true,
			supportsThinking: true,
			thinkingLevel: 1,
			maxTokens: 1048576,
			maxOutputTokens: 65535,
			model: "MODEL_PLACEHOLDER_M36",
			apiProvider: "API_PROVIDER_GOOGLE_VERTEX",
			modelProvider: "MODEL_PROVIDER_GOOGLE",
		},
		"gemini-pro-agent": {
			displayName: "Gemini Pro Agent",
			supportsImages: true,
			supportsThinking: true,
			thinkingBudget: 10001,
			maxTokens: 1048576,
			maxOutputTokens: 65535,
			model: "MODEL_PLACEHOLDER_M16",
		},
		"claude-sonnet-4-6-thinking": {
			displayName: "Claude Sonnet 4.6 (Thinking)",
			supportsImages: true,
			supportsThinking: true,
			thinkingBudget: 1024,
			maxTokens: 250000,
			maxOutputTokens: 64000,
		},
		"chat_20706": { displayName: "denylisted" },
		"internal-row": { displayName: "internal", isInternal: true },
		"gemini-4-argon-disabled": { displayName: "unavailable", disabled: true },
	},
	agentModelSorts: [{ groups: [{ modelIds: ["gemini-3.1-pro-low"] }] }],
	imageGenerationModelIds: ["gemini-3-pro-image"],
};

type Answer = { status?: number; body?: string; throw?: true };

const scenarios: { name: string; projectId?: string; answers: Answer[] }[] = [
	{ name: "daily answers", projectId: "capture-project", answers: [{ body: JSON.stringify(ROSTER) }] },
	{ name: "no project", answers: [{ body: JSON.stringify(ROSTER) }] },
	{ name: "daily 500 then sandbox", answers: [{ status: 500 }, { body: JSON.stringify(ROSTER) }] },
	{ name: "daily 401 then sandbox", answers: [{ status: 401 }, { body: JSON.stringify(ROSTER) }] },
	{ name: "both rejected 403", answers: [{ status: 403 }, { status: 403 }] },
	{ name: "daily 401 sandbox 500", answers: [{ status: 401 }, { status: 500 }] },
	{ name: "daily invalid json", answers: [{ body: "{not json" }, { body: JSON.stringify(ROSTER) }] },
	{ name: "daily non-object json", answers: [{ body: "[]" }, { body: JSON.stringify(ROSTER) }] },
	{ name: "daily network error", answers: [{ throw: true }, { body: JSON.stringify(ROSTER) }] },
	{ name: "both network errors", answers: [{ throw: true }, { throw: true }] },
	{ name: "empty roster", answers: [{ body: "{}" }] },
	{ name: "daily 429 then sandbox", answers: [{ status: 429 }, { body: JSON.stringify(ROSTER) }] },
];

const ENDPOINTS = [ANTIGRAVITY_PRIMARY_ENDPOINT, ANTIGRAVITY_SANDBOX_ENDPOINT];

const results = [];
for (const scenario of scenarios) {
	const requests: unknown[] = [];
	const stub = Object.assign(
		async (input: any, init: any) => {
			const url = typeof input === "string" ? input : input.url;
			const headers = Object.fromEntries(
				Object.entries(init?.headers ?? {}).map(([key, value]) => [key.toLowerCase(), String(value)]),
			);
			requests.push({
				method: init?.method,
				url,
				authorization: headers.authorization,
				contentType: headers["content-type"],
				userAgent: headers["user-agent"],
				acceptEncoding: headers["accept-encoding"],
				body: init?.body,
			});
			const index = ENDPOINTS.indexOf(new URL(url).origin);
			const answer = scenario.answers[index] ?? { status: 599 };
			if (answer.throw) throw new Error("network down");
			return new Response(answer.body ?? "", { status: answer.status ?? 200 });
		},
		{ preconnect: () => {} },
	);
	const result = await fetchAntigravityDiscoveryModels({
		token: "capture-token",
		projectId: scenario.projectId,
		fetcher: stub as any,
	});
	results.push({
		name: scenario.name,
		projectId: scenario.projectId ?? null,
		requests,
		rejectedStatus: result?.rejectedStatus ?? null,
		models: result === null ? null : result.models.map((model: any) => model.id),
	});
}

console.log(JSON.stringify({ source: "NeoPi OWN fetchAntigravityDiscoveryModels over scripted endpoints", scenarios: results }, null, 1));
