// Runs NeoPi's OWN Antigravity model discovery end to end: the model manager
// (createModelManager over googleAntigravityModelManagerOptions) merges
// fetchAntigravityDiscoveryModels' normalized and collapsed roster over the
// bundled seed, exactly as NeoPi's agent resolves its google-antigravity
// catalog. Each scenario serves one scripted fetchAvailableModels payload from
// a stub fetch and records the NeoPiModel records that come out.
//
//   bun capture_antigravity_npi_discovery.ts | gzip -9 -n > antigravity_npi_discovery.json.gz
//
// The scenarios cover the whole roster plus every row alone, seeded random
// subsets (each collapse family is met with every subset of its members) and
// ordering rows that pin JS key order and `localeCompare` sorting.
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
process.env.PI_AI_ANTIGRAVITY_VERSION = "2.26.0";
process.env.PI_AI_ANTIGRAVITY_OS = "linux";
process.env.PI_AI_ANTIGRAVITY_ARCH = "amd64";

const { createModelManager } = await import(root + "/pi-catalog/src/model-manager.ts");
const { googleAntigravityModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/google.ts");
const { neoPiRecord } = await import(import.meta.dir + "/capture_antigravity_npi_models.ts");

const DAILY = "https://daily-cloudcode-pa.googleapis.com";
const SANDBOX = "https://daily-cloudcode-pa.sandbox.googleapis.com";

type Row = Record<string, unknown>;
const GEMINI_CTX = 1048576;
const row = (id: string, over: Row = {}): [string, Row] => [
	id,
	{
		displayName: id,
		supportsImages: true,
		supportsThinking: true,
		maxTokens: GEMINI_CTX,
		maxOutputTokens: 65535,
		model: `MODEL_${id.toUpperCase().replace(/[^A-Z0-9]+/g, "_")}`,
		apiProvider: "API_PROVIDER_GOOGLE_VERTEX",
		modelProvider: "MODEL_PROVIDER_GOOGLE",
		...over,
	},
];
const claude = (id: string, display: string, over: Row = {}): [string, Row] =>
	row(id, {
		displayName: display,
		maxTokens: 200000,
		maxOutputTokens: 64000,
		apiProvider: "API_PROVIDER_ANTHROPIC_VERTEX",
		modelProvider: "MODEL_PROVIDER_ANTHROPIC",
		...over,
	});

const ROSTER: [string, Row][] = [
	row("gemini-2.5-flash", { displayName: "Gemini 2.5 Flash", thinkingBudget: 24576 }),
	row("gemini-2.5-flash-thinking", { displayName: "Gemini 2.5 Flash (Thinking)", thinkingBudget: 8192 }),
	row("gemini-2.5-flash-lite", { displayName: "Gemini 2.5 Flash Lite", supportsThinking: false }),
	row("gemini-2.5-pro", { displayName: "Gemini 2.5 Pro" }),
	row("gemini-3-pro-low", { displayName: "Gemini 3 Pro (Low)", thinkingLevel: 1 }),
	row("gemini-3-pro-high", { displayName: "Gemini 3 Pro (High)", thinkingLevel: 3 }),
	row("gemini-3-pro-image", { displayName: "Gemini 3 Pro Image", supportsThinking: false, maxOutputTokens: 32768 }),
	row("gemini-3.1-flash-image", { displayName: "Gemini 3.1 Flash Image", supportsThinking: false }),
	row("gemini-3.1-flash-lite", { displayName: "Gemini 3.1 Flash Lite", thinkingLevel: 2 }),
	row("gemini-3.1-pro-low", { displayName: "Gemini 3.1 Pro (Low)", thinkingLevel: 1, recommended: true }),
	row("gemini-3.1-pro-high", { displayName: "Gemini 3.1 Pro (High)", thinkingLevel: 3 }),
	row("gemini-pro-agent", { displayName: "Gemini Pro Agent", thinkingBudget: 10001 }),
	row("gemini-3.5-flash-extra-low", { displayName: "Gemini 3.5 Flash (Extra Low)", thinkingBudget: 0, maxOutputTokens: 65536 }),
	row("gemini-3.5-flash-low", { displayName: "Gemini 3.5 Flash (Low)", thinkingBudget: 1000, maxOutputTokens: 65536 }),
	row("gemini-3-flash-agent", { displayName: "Gemini 3 Flash Agent", thinkingBudget: -1, maxOutputTokens: 65536 }),
	row("gemini-3-flash", { displayName: "Gemini 3 Flash" }),
	row("gemini-3.5-flash-lite", { displayName: "Gemini 3.5 Flash Lite", thinkingLevel: 1 }),
	...["3.6", "3.7", "3.8"].flatMap(rev => [
		row(`gemini-${rev}-flash-low`, { displayName: `Gemini ${rev} Flash (Low)`, thinkingLevel: 1, maxOutputTokens: 65536 }),
		row(`gemini-${rev}-flash-medium`, { displayName: `Gemini ${rev} Flash (Medium)`, thinkingLevel: 2, maxOutputTokens: 65536 }),
		row(`gemini-${rev}-flash-high`, { displayName: `Gemini ${rev} Flash (High)`, thinkingLevel: 3, maxOutputTokens: 65536 }),
		row(`gemini-${rev}-flash-tiered`, { displayName: `Gemini ${rev} Flash`, maxOutputTokens: 65536 }),
	]),
	row("gemini-3.9-flash-low", { displayName: "Gemini 3.9 Flash (Low)", thinkingLevel: 1 }),
	row("gemini-3.9-flash-tiered", { displayName: "Gemini 3.9 Flash" }),
	row("gemini-3.2-flash-low", { displayName: "Gemini 3.2 Flash (Low)", thinkingLevel: 1 }),
	row("gemini-4-argon-low", { displayName: "Gemini 4 Argon (Low)", thinkingLevel: 1 }),
	row("gemini-4-argon-medium", { displayName: "Gemini 4 Argon (Medium)", thinkingLevel: 2 }),
	row("gemini-4-argon-high", { displayName: "Gemini 4 Argon (High)", thinkingLevel: 3 }),
	row("gemini-4-argon-disabled", { displayName: "Gemini 4 Argon", disabled: true, model: "MODEL_PLACEHOLDER_M421" }),
	row("gemini-3.1-pro", { displayName: "Gemini 3.1 Pro (Bare)" }),
	claude("claude-opus-4-5", "Claude Opus 4.5", { supportsThinking: false, maxOutputTokens: 65536 }),
	claude("claude-opus-4-5-thinking", "Claude Opus 4.5 (Thinking)", { thinkingBudget: 16000, maxOutputTokens: 65536 }),
	claude("claude-sonnet-4-5", "Claude Sonnet 4.5", { supportsThinking: false, maxTokens: 1000000 }),
	claude("claude-sonnet-4-5-thinking", "Claude Sonnet 4.5 (Thinking)", { thinkingBudget: 16000, maxTokens: 1000000 }),
	claude("claude-sonnet-4-6", "Claude Sonnet 4.6 (Thinking)", { maxTokens: 250000 }),
	claude("claude-sonnet-4-6-thinking", "Claude Sonnet 4.6 (Thinking)", { maxTokens: 250000, thinkingBudget: 1024 }),
	claude("claude-opus-4-6-thinking", "Claude Opus 4.6 (Thinking)", { maxTokens: 250000, thinkingBudget: 1024 }),
	claude("claude-opus-4-6", "Claude Opus 4.6", { maxTokens: 250000, supportsThinking: false }),
	claude("claude-haiku-4-5", "Claude Haiku 4.5", { supportsThinking: false }),
	claude("claude-opus-4-7", "Claude Opus 4.7", { maxOutputTokens: 128000 }),
	claude("claude-opus-4-7-thinking", "Claude Opus 4.7 (Thinking)", { maxOutputTokens: 128000, thinkingBudget: 2048 }),
	...["opus", "sonnet"].flatMap(family =>
		[
			["low", 1, "M400"],
			["medium", 2, "M401"],
			["high", 3, "M402"],
		].map(([tier, level, enumId]) =>
			claude(`claude-${family}-5-5-${tier}`, `Claude ${family === "opus" ? "Opus" : "Sonnet"} 5.5 (${tier})`, {
				maxTokens: 1000000,
				maxOutputTokens: 128000,
				thinkingLevel: level,
				model: `MODEL_PLACEHOLDER_${enumId}`,
			}),
		),
	),
	row("gpt-oss-120b-medium", {
		displayName: "GPT-OSS 120B (Medium)",
		maxTokens: 131072,
		maxOutputTokens: 32768,
		supportsImages: false,
		apiProvider: "API_PROVIDER_OPENAI_VERTEX",
		modelProvider: "MODEL_PROVIDER_OPENAI",
	}),
	row("tab_flash_lite_preview", { displayName: "Tab Flash Lite", supportsThinking: false, supportsImages: false, maxTokens: 16384, maxOutputTokens: 4096 }),
	row("tab_jump_flash_lite_preview", { displayName: "Tab Jump Flash Lite", supportsThinking: false, supportsImages: false, maxTokens: 16384, maxOutputTokens: 4096 }),
	row("gemma-3-27b", { displayName: "Gemma 3 27B", supportsThinking: false, supportsImages: false, maxOutputTokens: 8192 }),
	row("chat_20706", { displayName: "chat_20706" }),
	row("chat_23310", { displayName: "chat_23310" }),
	row("chat_40001", { displayName: "Internal chat", isInternal: true }),
	row("foo-model", { displayName: "Foo Model", supportsThinking: false }),
	row("foo-model-thinking", { displayName: "Foo Model (Thinking)" }),
	row("mystery-sku", { displayName: "", supportsThinking: true, maxTokens: 0, maxOutputTokens: 0 }),
	row("no-meta", { displayName: undefined, supportsImages: undefined, supportsThinking: undefined, maxTokens: undefined, maxOutputTokens: undefined, model: undefined }),
];

// Junk the schema must drop rather than fail on.
const JUNK: [string, Row][] = [
	row("junk-types", {
		displayName: 7,
		supportsImages: "yes",
		supportsThinking: 1,
		thinkingBudget: "100",
		thinkingLevel: Number.NaN,
		maxTokens: "5",
		maxOutputTokens: null,
		model: 5,
		isInternal: "true",
		disabled: "true",
	}),
	row("gemini-3.1-pro-low", { thinkingLevel: 7, thinkingBudget: 0, maxOutputTokens: -1 }),
];

// Ids and names that pin key order and the English collation: integer-like
// keys iterate first and ascending whatever their insertion order, and the
// final sort compares display names with localeCompare (case, accents,
// punctuation, digits) before ids.
const ORDERING: [string, Row][] = [
	row("zz", { displayName: "\u00dcn\u00efcode", supportsThinking: false }),
	row("10", { displayName: "Zeta Model", supportsThinking: false }),
	row("b-model", { displayName: "\u00e9clair", supportsThinking: false }),
	row("2", { displayName: "alpha model", supportsThinking: false }),
	row("a-model", { displayName: "\u00c9clair", supportsThinking: false }),
	row("1", { displayName: "\u00c5ngstr\u00f6m", supportsThinking: false }),
	row("_x", { displayName: "_under", supportsThinking: false }),
	row("same-1", { displayName: "Same Name", supportsThinking: false }),
	row("same-0", { displayName: "Same Name", supportsThinking: false }),
	row("9a", { displayName: "10 items", supportsThinking: false }),
	row("8a", { displayName: "9 items", supportsThinking: false }),
	row("a-id", { displayName: "A-B", supportsThinking: false }),
	row("A-id", { displayName: "a-b", supportsThinking: false }),
	row("lower", { displayName: "apple", supportsThinking: false }),
	row("upper", { displayName: "Apple", supportsThinking: false }),
	// Equal names fall back to `id.localeCompare`, not byte order.
	row("Tie", { displayName: "Tied", supportsThinking: false }),
	row("tie", { displayName: "Tied", supportsThinking: false }),
	row("tie-2", { displayName: "Tied", supportsThinking: false }),
	row("tie_2", { displayName: "Tied", supportsThinking: false }),
	row("tie2", { displayName: "Tied", supportsThinking: false }),
	row("tie-10", { displayName: "Tied", supportsThinking: false }),
	row("tie-9", { displayName: "Tied", supportsThinking: false }),
];

// Deterministic PRNG so the subset scenarios are reproducible.
let seed = 0x2f6e2b1;
const random = () => {
	seed = (seed * 1664525 + 1013904223) >>> 0;
	return seed / 0x100000000;
};

const scenarios: { name: string; endpoint: string; payload: unknown }[] = [];
const payloadOf = (rows: [string, Row][]) => ({
	models: Object.fromEntries(rows),
	agentModelSorts: [{ groups: [{ modelIds: rows.slice(0, 3).map(([id]) => id) }] }],
	imageGenerationModelIds: ["gemini-3-pro-image"],
});
scenarios.push({ name: "full roster", endpoint: DAILY, payload: payloadOf(ROSTER) });
scenarios.push({ name: "full roster via sandbox", endpoint: SANDBOX, payload: payloadOf(ROSTER) });
scenarios.push({ name: "junk rows", endpoint: DAILY, payload: payloadOf([...JUNK, ...ROSTER.slice(0, 6)]) });
scenarios.push({ name: "empty roster", endpoint: DAILY, payload: { models: {} } });
scenarios.push({ name: "no models key", endpoint: DAILY, payload: {} });
scenarios.push({ name: "non-object model entries", endpoint: DAILY, payload: { models: { a: 1, b: null, c: "x", "gemini-3.1-pro-low": ROSTER[9][1] } } });
scenarios.push({ name: "ordering rows", endpoint: DAILY, payload: payloadOf(ORDERING) });
scenarios.push({ name: "ordering rows beside the roster", endpoint: DAILY, payload: payloadOf([...ORDERING, ...ROSTER]) });
scenarios.push({ name: "ordering rows reversed", endpoint: DAILY, payload: payloadOf([...ORDERING].reverse()) });
for (const [id, fields] of ROSTER) {
	scenarios.push({ name: `only ${id}`, endpoint: DAILY, payload: payloadOf([[id, fields]]) });
}
for (let i = 0; i < 120; i++) {
	const rows = ROSTER.filter(() => random() < 0.35);
	// Shuffle: the collapse and merge are order-sensitive.
	for (let j = rows.length - 1; j > 0; j--) {
		const k = Math.floor(random() * (j + 1));
		[rows[j], rows[k]] = [rows[k], rows[j]];
	}
	scenarios.push({ name: `random subset ${i}`, endpoint: random() < 0.3 ? SANDBOX : DAILY, payload: payloadOf(rows) });
}

const results: unknown[] = [];
const dir = mkdtempSync(join(tmpdir(), "antigravity-npi-discovery-"));
try {
	for (const [index, scenario] of scenarios.entries()) {
		const stub = Object.assign(
			async (input: any) => {
				const url = typeof input === "string" ? input : input.url;
				const host = new URL(url).origin;
				if (host !== scenario.endpoint) return new Response("unavailable", { status: 503 });
				return new Response(JSON.stringify(scenario.payload), { headers: { "content-type": "application/json" } });
			},
			{ preconnect: () => {} },
		);
		const options = {
			...googleAntigravityModelManagerOptions({
				resolveAccounts: async () => [{ accessToken: "capture-token", projectId: "capture-project" }],
				fetch: stub,
			}),
			cacheDbPath: join(dir, `${index}.db`),
		};
		const result = await createModelManager(options).refresh("online");
		results.push({
			name: scenario.name,
			endpoint: scenario.endpoint,
			payload: scenario.payload,
			source: result.source,
			models: result.models.map(neoPiRecord),
		});
	}
} finally {
	rmSync(dir, { recursive: true, force: true });
}

console.log(
	JSON.stringify({ source: "NeoPi OWN createModelManager(googleAntigravityModelManagerOptions) over scripted rosters", scenarios: results }),
);
