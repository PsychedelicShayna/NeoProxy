import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import type { OAuthLoginCallbacks } from "@oh-my-pi/pi-ai";
import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";

// Marineris: OAuth-backed gateway (xAI + Codex) with scoped gateway keys.
// Key resolution, first hit wins:
//   MARINERIS_BASE_URL / MARINERIS_API_KEY environment variables
//   ~/.omp/agent/extensions/marineris/config.json  { "baseUrl": "...", "apiKey": "..." }
//   ./config.json next to this file
//   /login → Marineris (prompts for the gateway key, stored in omp's auth store)
const PROVIDER_ID = "marineris";
const DEFAULT_BASE_URL = "http://192.168.50.50:8801/v1";
const REQUEST_TIMEOUT_MS = 10_000;

type Api = "openai-completions" | "openai-responses";
type Effort = "minimal" | "low" | "medium" | "high" | "xhigh" | "max";

type Config = { baseUrl: string; apiKey: string };

type MappedModel = {
	id: string;
	name: string;
	api: Api;
	reasoning: boolean;
	thinking?: { mode: "effort"; efforts: Effort[]; defaultLevel?: Effort };
	input: ("text" | "image")[];
	cost: { input: number; output: number; cacheRead: number; cacheWrite: number };
	contextWindow: number;
	maxTokens: number;
	compat?: Record<string, unknown>;
};

function readJson(path: string): Record<string, unknown> {
	try {
		return existsSync(path) ? (JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>) : {};
	} catch {
		return {};
	}
}

const USER_CONFIG_PATH = join(homedir(), ".omp", "agent", "extensions", "marineris", "config.json");

function normalizeBaseUrl(value: string): string {
	const trimmed = value.trim().replace(/\/+$/, "");
	const withScheme = /^https?:\/\//.test(trimmed) ? trimmed : `http://${trimmed}`;
	return withScheme.endsWith("/v1") ? withScheme : `${withScheme}/v1`;
}

function loadConfig(): Config {
	const file = {
		...readJson(join(import.meta.dir, "config.json")),
		...readJson(USER_CONFIG_PATH),
	};
	const baseUrl = process.env.MARINERIS_BASE_URL ?? (file.baseUrl as string | undefined) ?? DEFAULT_BASE_URL;
	const apiKey = process.env.MARINERIS_API_KEY ?? (file.apiKey as string | undefined) ?? "";
	return { baseUrl: normalizeBaseUrl(baseUrl), apiKey };
}

const config = loadConfig();

function num(value: unknown): number | undefined {
	return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : undefined;
}

// xAI prices are in ten-thousandths of a cent per token; /10000 gives USD per million.
function usdPerMillion(value: unknown): number {
	return typeof value === "number" && value > 0 ? value / 10_000 : 0;
}

function mapModel(row: Record<string, unknown>): MappedModel | undefined {
	const id = typeof row.id === "string" ? row.id : undefined;
	if (!id) return undefined;
	const provider = String(row.provider ?? row.owned_by ?? "");
	const codex = provider === "codex" || id.startsWith("gpt-") || id.startsWith("codex") || id.startsWith("o");
	const reasoning = codex || /reasoning/.test(id) && !/non-reasoning/.test(id);
	const contextWindow = num(row.context_window) ?? num(row.context_length) ?? (codex ? 400_000 : 131_072);
	const maxTokens = num(row.max_output_tokens) ?? num(row.max_tokens) ?? Math.min(128_000, Math.floor(contextWindow / 4));
	const image = num(row.prompt_image_token_price) !== undefined || Array.isArray(row.input_modalities) && (row.input_modalities as unknown[]).includes("image");
	return {
		id,
		name: typeof row.name === "string" ? row.name : id,
		api: codex ? "openai-responses" : "openai-completions",
		reasoning,
		...(reasoning
			? { thinking: { mode: "effort" as const, efforts: codex ? ["low", "medium", "high", "xhigh"] as Effort[] : ["low", "high"] as Effort[], defaultLevel: codex ? "medium" as const : "high" as const } }
			: {}),
		input: image ? ["text", "image"] : ["text"],
		cost: {
			input: usdPerMillion(row.prompt_text_token_price),
			output: usdPerMillion(row.completion_text_token_price),
			cacheRead: usdPerMillion(row.cached_prompt_text_token_price),
			cacheWrite: 0,
		},
		contextWindow,
		maxTokens,
		...(codex ? {} : { compat: { supportsStore: false, supportsDeveloperRole: false, maxTokensField: "max_tokens" } }),
	};
}

async function fetchModels(apiKey: string): Promise<MappedModel[]> {
	const controller = new AbortController();
	const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
	try {
		const response = await fetch(`${config.baseUrl}/models`, {
			headers: { Accept: "application/json", Authorization: `Bearer ${apiKey}` },
			signal: controller.signal,
		});
		if (!response.ok) throw new Error(`Marineris /v1/models returned HTTP ${response.status}`);
		const data = ((await response.json()) as { data?: unknown }).data;
		if (!Array.isArray(data)) throw new Error("Marineris returned an invalid model catalog");
		return data
			.filter((row): row is Record<string, unknown> => typeof row === "object" && row !== null)
			.map(mapModel)
			.filter((model): model is MappedModel => Boolean(model));
	} finally {
		clearTimeout(timer);
	}
}

async function login(callbacks: OAuthLoginCallbacks): Promise<string> {
	const baseUrl = normalizeBaseUrl(
		(await callbacks.onPrompt({
			message: `Marineris gateway URL (Enter keeps ${config.baseUrl})`,
			placeholder: config.baseUrl,
			allowEmpty: true,
		})) || config.baseUrl,
	);
	const key = (await callbacks.onPrompt({
		message: `Gateway key for ${baseUrl} (mrn_...)`,
		placeholder: "mrn_",
	})).trim();
	if (!key) throw new Error("No gateway key entered");
	config.baseUrl = baseUrl;
	const models = await fetchModels(key);
	if (models.length === 0) throw new Error("Key accepted but no models are visible to it; check its gates");
	if (!process.env.MARINERIS_BASE_URL) {
		mkdirSync(dirname(USER_CONFIG_PATH), { recursive: true });
		writeFileSync(USER_CONFIG_PATH, JSON.stringify({ ...readJson(USER_CONFIG_PATH), baseUrl }, null, 2) + "\n", { mode: 0o600 });
	}
	return key;
}

export default function (pi: ExtensionAPI) {
	const register = () =>
		pi.registerProvider(PROVIDER_ID, {
			baseUrl: config.baseUrl,
			api: "openai-completions",
			...(config.apiKey ? { apiKey: config.apiKey } : {}),
			authHeader: true,
			oauth: {
				name: "Marineris gateway",
				login: async (callbacks) => {
					const previous = config.baseUrl;
					const key = await login(callbacks);
					if (config.baseUrl !== previous) register();
					return key;
				},
				getApiKey: (credential) => credential.access,
			},
			fetchDynamicModels: async (apiKey) => {
				const key = apiKey || config.apiKey;
				if (!key) return [];
				return fetchModels(key);
			},
		});
	register();
}
