import type { OAuthCredentials, OAuthLoginCallbacks } from "@oh-my-pi/pi-ai";
import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";

const PROVIDER_ID = "nous-portal";
const PROVIDER_NAME = "Nous Research Portal";
const PORTAL_URL = "https://portal.nousresearch.com";
const INFERENCE_URL = "https://inference-api.nousresearch.com/v1";
const CLIENT_ID = "hermes-cli";
const SCOPE = "inference:invoke";
const DEVICE_GRANT = "urn:ietf:params:oauth:grant-type:device_code";
const REQUEST_TIMEOUT_MS = 15_000;
const THINKING_EFFORTS_ORDER = [
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
] as const;

type ThinkingEffort = (typeof THINKING_EFFORTS_ORDER)[number];
type ThinkingConfig = {
	mode: "effort";
	efforts: ThinkingEffort[];
	defaultLevel?: ThinkingEffort;
};

export type MappedModel = {
	id: string;
	name: string;
	api: "openai-completions";
	reasoning: boolean;
	thinking?: ThinkingConfig;
	input: ("text" | "image")[];
	supportsTools?: boolean;
	cost: {
		input: number;
		output: number;
		cacheRead: number;
		cacheWrite: number;
	};
	contextWindow: number;
	maxTokens: number;
	compat: {
		supportsStore: false;
		supportsDeveloperRole: false;
		maxTokensField: "max_tokens";
		thinkingFormat?: "openrouter";
	};
};

type Enrichment = {
	reasoning?: boolean;
	thinking?: ThinkingConfig | null;
	input?: ("text" | "image")[];
	maxTokens?: number | null;
	supportsTools?: boolean;
};

type ModelRow = {
	id?: unknown;
	name?: unknown;
	context_length?: unknown;
	architecture?: {
		modality?: unknown;
		output_modalities?: unknown;
	};
	pricing?: Record<string, unknown>;
	reasoning?: {
		supported_efforts?: unknown;
		default_effort?: unknown;
	} | null;
	supported_parameters?: unknown;
	top_provider?: {
		max_completion_tokens?: unknown;
	};
};

type TokenResponse = {
	access_token?: unknown;
	refresh_token?: unknown;
	expires_in?: unknown;
};

type DeviceCode = {
	deviceCode: string;
	userCode: string;
	verificationUrl: string;
	expiresIn: number;
	interval: number;
};

function loadJson<T>(path: string, fallback: T): T {
	try {
		return require(path) as T;
	} catch {
		return fallback;
	}
}

const enrichment = loadJson<Record<string, Enrichment>>("./enrichment.json", {});
const snapshot = loadJson<MappedModel[]>("./snapshot.json", []);
const PINNED_MODELS: MappedModel[] = [];

function record(value: unknown): Record<string, unknown> {
	return typeof value === "object" && value !== null
		? value as Record<string, unknown>
		: {};
}

function string(value: unknown): string | undefined {
	return typeof value === "string" && value.trim()
		? value.trim()
		: undefined;
}

function positiveInteger(value: unknown): number | undefined {
	const number = typeof value === "string" ? Number(value) : value;
	return typeof number === "number" && Number.isFinite(number) && number > 0
		? Math.floor(number)
		: undefined;
}

function price(value: unknown): number {
	const parsed = Number.parseFloat(String(value ?? "0"));
	return Number.isFinite(parsed) ? parsed * 1_000_000 : 0;
}

function enrichmentFor(id: string): Enrichment {
	return enrichment[id]
		?? enrichment[id.replace(/:(?:free|thinking)$/, "")]
		?? {};
}

function thinkingFromRow(value: unknown): ThinkingConfig | undefined {
	const reasoning = record(value);
	const supported = Array.isArray(reasoning.supported_efforts)
		? reasoning.supported_efforts.filter((effort): effort is string => typeof effort === "string")
		: [];
	const efforts = THINKING_EFFORTS_ORDER.filter((effort) => supported.includes(effort));
	if (efforts.length === 0) return undefined;

	const defaultEffort = string(reasoning.default_effort) as ThinkingEffort | undefined;
	return {
		mode: "effort",
		efforts,
		...(defaultEffort && efforts.includes(defaultEffort) ? { defaultLevel: defaultEffort } : {}),
	};
}

export function mapRow(value: unknown): MappedModel | undefined {
	const row = record(value) as ModelRow;
	const id = string(row.id);
	if (!id || id.endsWith(":batch")) return undefined;

	const architecture = record(row.architecture);
	const outputModalities = architecture.output_modalities;
	if (Array.isArray(outputModalities) && !outputModalities.includes("text")) return undefined;

	const extra = enrichmentFor(id);
	const contextWindow = positiveInteger(row.context_length) ?? 131_072;
	const supportedParameters = Array.isArray(row.supported_parameters)
		? row.supported_parameters.filter((parameter): parameter is string => typeof parameter === "string")
		: [];
	const reasoning = supportedParameters.includes("reasoning")
		? true
		: extra.reasoning ?? false;
	const rowThinking = thinkingFromRow(row.reasoning);
	const thinking = rowThinking ?? extra.thinking ?? undefined;
	const modality = string(architecture.modality);
	const input = modality?.includes("image")
		? ["text", "image"] as const
		: extra.input ?? ["text"];
	const pricing = record(row.pricing);
	const topProvider = record(row.top_provider);
	const maxTokens = positiveInteger(topProvider.max_completion_tokens)
		?? positiveInteger(extra.maxTokens)
		?? Math.min(65_536, Math.floor(contextWindow / 4));

	return {
		id,
		name: string(row.name) ?? id,
		api: "openai-completions",
		reasoning,
		...(thinking ? { thinking } : {}),
		input: [...input],
		...(supportedParameters.length > 0 && !supportedParameters.includes("tools")
			? { supportsTools: false }
			: {}),
		cost: {
			input: price(pricing.prompt),
			output: price(pricing.completion),
			cacheRead: price(pricing.input_cache_read),
			cacheWrite: price(pricing.input_cache_write),
		},
		contextWindow,
		maxTokens,
		compat: {
			supportsStore: false,
			supportsDeveloperRole: false,
			maxTokensField: "max_tokens",
			...(reasoning ? { thinkingFormat: "openrouter" as const } : {}),
		},
	};
}

function unionById(models: readonly MappedModel[], pinned: readonly MappedModel[]): MappedModel[] {
	const result = new Map(models.map((model) => [model.id, model]));
	for (const model of pinned) result.set(model.id, model);
	return [...result.values()];
}

function fallbackCatalog(): MappedModel[] {
	return unionById(snapshot, PINNED_MODELS);
}

function requestSignal(parent?: AbortSignal): {
	signal: AbortSignal;
	cleanup: () => void;
} {
	const controller = new AbortController();
	const timeout = setTimeout(
		() => controller.abort(new Error("Nous Portal request timed out")),
		REQUEST_TIMEOUT_MS,
	);
	const abort = () => controller.abort(parent?.reason);

	if (parent?.aborted) abort();
	else parent?.addEventListener("abort", abort, { once: true });

	return {
		signal: controller.signal,
		cleanup: () => {
			clearTimeout(timeout);
			parent?.removeEventListener("abort", abort);
		},
	};
}

async function portalFetch(
	path: string,
	init: RequestInit,
	parent?: AbortSignal,
): Promise<Response> {
	const { signal, cleanup } = requestSignal(parent);
	try {
		return await fetch(`${PORTAL_URL}${path}`, { ...init, signal });
	} finally {
		cleanup();
	}
}

async function postForm(
	path: string,
	fields: Record<string, string>,
	signal?: AbortSignal,
	headers?: Record<string, string>,
): Promise<Response> {
	return portalFetch(path, {
		method: "POST",
		headers: {
			Accept: "application/json",
			"Content-Type": "application/x-www-form-urlencoded",
			...headers,
		},
		body: new URLSearchParams(fields).toString(),
	}, signal);
}

async function responseError(response: Response, fallback: string): Promise<Error> {
	const body = record(await response.json().catch(() => ({})));
	const code = string(body.error);
	const description = string(body.error_description) ?? string(body.message);
	return new Error(`${fallback} (${response.status})${code ? `: ${code}` : ""}${description ? `: ${description}` : ""}`);
}

async function sleep(ms: number, signal?: AbortSignal): Promise<void> {
	return new Promise((resolve, reject) => {
		if (signal?.aborted) {
			reject(signal.reason ?? new Error("Login cancelled"));
			return;
		}

		const abort = () => {
			clearTimeout(timer);
			reject(signal?.reason ?? new Error("Login cancelled"));
		};
		const timer = setTimeout(() => {
			signal?.removeEventListener("abort", abort);
			resolve();
		}, ms);
		signal?.addEventListener("abort", abort, { once: true });
	});
}

function credentials(token: TokenResponse, previous?: OAuthCredentials): OAuthCredentials {
	const access = string(token.access_token);
	const refreshToken = string(token.refresh_token) ?? previous?.refresh;
	if (!access) throw new Error("Nous Portal token response omitted access_token");
	if (!refreshToken) throw new Error("Nous Portal token response omitted refresh_token");

	const ttl = positiveInteger(token.expires_in) ?? 3600;
	const skew = Math.min(120, Math.floor(ttl / 10));
	return {
		refresh: refreshToken,
		access,
		expires: Date.now() + (ttl - skew) * 1000,
	};
}

async function requestDeviceCode(signal?: AbortSignal): Promise<DeviceCode> {
	const response = await postForm("/api/oauth/device/code", {
		client_id: CLIENT_ID,
		scope: SCOPE,
	}, signal);
	if (!response.ok) throw await responseError(response, "Device-code request failed");

	const body = record(await response.json());
	const verificationUri = string(body.verification_uri_complete)
		?? string(body.verification_uri);
	const verificationUrl = verificationUri ? new URL(verificationUri) : undefined;
	if (
		!verificationUrl
		|| verificationUrl.protocol !== "https:"
		|| verificationUrl.hostname !== "portal.nousresearch.com"
	) {
		throw new Error("Nous Portal returned an unexpected verification URL");
	}

	const deviceCode = string(body.device_code);
	const userCode = string(body.user_code);
	if (!deviceCode || !userCode) {
		throw new Error("Nous Portal returned an incomplete device code");
	}

	return {
		deviceCode,
		userCode,
		verificationUrl: verificationUrl.toString(),
		expiresIn: positiveInteger(body.expires_in) ?? 600,
		interval: positiveInteger(body.interval) ?? 5,
	};
}

async function pollForToken(
	device: DeviceCode,
	callbacks: OAuthLoginCallbacks,
): Promise<TokenResponse> {
	const deadline = Date.now() + device.expiresIn * 1000;
	let interval = Math.min(device.interval, 30);

	while (Date.now() < deadline) {
		callbacks.onProgress?.("Waiting for Nous Portal authorization…");
		const response = await postForm("/api/oauth/token", {
			grant_type: DEVICE_GRANT,
			client_id: CLIENT_ID,
			device_code: device.deviceCode,
		}, callbacks.signal);
		if (response.ok) return await response.json() as TokenResponse;

		const body = record(await response.json().catch(() => ({})));
		const code = string(body.error);
		if (code === "authorization_pending") {
			await sleep(interval * 1000, callbacks.signal);
			continue;
		}
		if (code === "slow_down") {
			interval = Math.min(interval + 1, 30);
			await sleep(interval * 1000, callbacks.signal);
			continue;
		}
		throw await responseError(
			new Response(JSON.stringify(body), {
				status: response.status,
				headers: { "content-type": "application/json" },
			}),
			"Device authorization failed",
		);
	}

	throw new Error("Nous Portal device authorization expired");
}

async function refresh(previous: OAuthCredentials): Promise<OAuthCredentials> {
	const response = await postForm(
		"/api/oauth/token",
		{ grant_type: "refresh_token", client_id: CLIENT_ID },
		undefined,
		{ "x-nous-refresh-token": previous.refresh },
	);
	if (!response.ok) throw await responseError(response, "Token refresh failed");
	return credentials(await response.json() as TokenResponse, previous);
}

async function login(callbacks: OAuthLoginCallbacks): Promise<OAuthCredentials | string> {
	const key = (await callbacks.onPrompt({
		message: "Paste a Nous Portal inference API key (leave empty to use the subscription OAuth device flow)",
		placeholder: "sk-…",
		allowEmpty: true,
	})).trim();
	if (key) return key;

	const device = await requestDeviceCode(callbacks.signal);
	callbacks.onAuth({
		url: device.verificationUrl,
		instructions: `Enter code ${device.userCode} at portal.nousresearch.com`,
	});
	return credentials(await pollForToken(device, callbacks));
}

async function fetchJson(url: string, apiKey: string): Promise<unknown[]> {
	const { signal, cleanup } = requestSignal();
	try {
		const response = await fetch(url, {
			headers: {
				Accept: "application/json",
				Authorization: `Bearer ${apiKey}`,
			},
			signal,
		});
		if (!response.ok) throw await responseError(response, "Model discovery failed");
		const data = record(await response.json()).data;
		if (!Array.isArray(data)) throw new Error("Nous Portal returned an invalid model catalog");
		return data;
	} finally {
		cleanup();
	}
}

export default function (pi: ExtensionAPI) {
	pi.registerProvider(PROVIDER_ID, {
		baseUrl: INFERENCE_URL,
		api: "openai-completions",
		oauth: {
			name: PROVIDER_NAME,
			login,
			refreshToken: refresh,
			getApiKey: (credential) => credential.access,
		},
		fetchDynamicModels: async (apiKey) => {
			if (!apiKey) return fallbackCatalog();
			try {
				const rows = await fetchJson(`${INFERENCE_URL}/models`, apiKey);
				const mapped = rows
					.map(mapRow)
					.filter((model): model is MappedModel => Boolean(model));
				return unionById(mapped, PINNED_MODELS);
			} catch {
				return fallbackCatalog();
			}
		},
	});
}
