// Runs NeoPi's OWN google-antigravity provider (streamSimple -> streamGoogleGeminiCli)
// against a loopback fake of Cloud Code Assist's streamGenerateContent, recording
// every request it sends and the assistant message it produces. crypto.randomUUID,
// Date.now and the client version are pinned so the envelope is reproducible.
//
//   cd <neopi env> && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     PI_AI_ANTIGRAVITY_VERSION=2.26.0 bun --preserve-symlinks capture_antigravity_npi.ts
//
// Writes antigravity_npi_capture.json beside this script.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");

const PROJECT = "capture-project-0001";
const credentials = { token: "capture-access-token", projectId: PROJECT, refreshToken: "capture-refresh", email: "capture@example.com", expiresAt: Date.now() + 3600_000 };
const apiKey = JSON.stringify(credentials);

// Pinned clock and ids.
const FIXED_NOW = 1_760_000_000_000;
Date.now = () => FIXED_NOW;
let uuidCounter = 0;
crypto.randomUUID = (() => `00000000-0000-4000-8000-${String(++uuidCounter).padStart(12, "0")}`);

let captured: any[] = [];
let script: any[] = [];
let received = 0;

const sse = (payloads: any[]) =>
	new Response(payloads.map(p => `data: ${JSON.stringify(p)}\n\n`).join(""), { headers: { "content-type": "text/event-stream" } });

const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const body = await request.text();
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), body });
		const step = script[Math.min(received, script.length - 1)];
		received++;
		if (step.status) {
			return new Response(step.body ?? "", { status: step.status, headers: { "content-type": "application/json" } });
		}
		return sse(step.chunks);
	},
});
const local = `http://127.0.0.1:${server.port}`;

// A built google-antigravity model: buildModel runs NeoPi's own compat engine.
const model = (id: string, extra: any = {}) =>
	buildModel({
		id,
		name: extra.name ?? id,
		api: "google-gemini-cli",
		provider: "google-antigravity",
		baseUrl: local,
		reasoning: true,
		input: ["text", "image"],
		cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
		contextWindow: 200000,
		maxTokens: 64000,
		...extra,
	} as any);

const models: Record<string, any> = {
	"gemini-3.1-pro": model("gemini-3.1-pro", { thinking: { mode: "budget", efforts: ["low", "high"], effortBudgets: { low: 1001, high: 10001 } } as any }),
	"gemini-3.7-flash": model("gemini-3.7-flash", {
		thinking: {
			mode: "google-level",
			efforts: ["minimal", "low", "medium", "high"],
			requiresEffort: true,
			effortRouting: { minimal: "gemini-3.7-flash-low", low: "gemini-3.7-flash-low", medium: "gemini-3.7-flash-medium", high: "gemini-3.7-flash-high" },
		} as any,
		compat: { antigravityWireProfiles: undefined },
	}),
	"gemini-3.5-flash": model("gemini-3.5-flash", {
		thinking: { mode: "budget", efforts: ["minimal", "low", "medium", "high"], effortBudgets: { minimal: 1000, low: 1000, medium: 4000, high: 10000 }, suppressWhenOff: true } as any,
	}),
	"claude-opus-5-5": model("claude-opus-5-5", {
		input: ["text", "image"],
		thinking: { mode: "budget", efforts: ["low", "medium", "high"], requiresEffort: true } as any,
	}),
	"gemini-3.1-flash-image": model("gemini-3.1-flash-image", { reasoning: false, input: ["text", "image"] }),
};

// A per-SKU wire profile, as discovery attaches it, with effort routing that
// selects the profiled wire id.
const profiled = model("gemini-3.1-pro", {
	requestModelId: "gemini-3.1-pro",
	thinking: { mode: "budget", efforts: ["low", "high"], effortBudgets: { low: 1001, high: 10001 }, effortRouting: { low: "gemini-3.1-pro-low", high: "gemini-3.1-pro-high" } } as any,
});
profiled.antigravityWireProfiles = { "gemini-3.1-pro-low": { modelEnum: "MODEL_PLACEHOLDER_M36", maxOutputTokens: 65535 } };
models["gemini-3.1-pro-profiled"] = profiled;

// Claude with the scoped thinking beta header enabled, as models.json marks it.
const claudeBeta = model("claude-opus-5-5", {
	thinking: { mode: "budget", efforts: ["low", "medium", "high"], requiresEffort: true } as any,
});
claudeBeta.compat = { ...claudeBeta.compat, claudeThinkingBetaHeader: true };
models["claude-opus-5-5-beta"] = claudeBeta;

const textPart = (text: string, extra: any = {}) => ({ text, ...extra });
const assistantText = (text: string, extra: any = {}) => ({
	response: {
		candidates: [{ content: { role: "model", parts: [textPart(text, extra)] }, finishReason: "STOP" }],
		usageMetadata: { promptTokenCount: 11, candidatesTokenCount: 5, thoughtsTokenCount: 2, totalTokenCount: 18, cachedContentTokenCount: 3 },
		modelVersion: "capture",
		responseId: "resp-capture",
	},
});

const scenarios: any[] = [];
const add = (name: string, modelId: string, context: any, options: any, scripted: any[]) =>
	scenarios.push({ name, model: modelId, context, options, script: scripted });

const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" }, limit: { type: ["number", "null"], description: "Max hits." } }, required: ["term"] } };
const user = (content: any) => ({ role: "user", content, timestamp: 0 });
const assistant = (m: any, content: any[], stopReason = "toolUse") => ({
	role: "assistant", content, api: "google-gemini-cli", provider: "google-antigravity", model: m.id,
	usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
	stopReason, timestamp: 0,
});
const result = (id: string, content: any[], isError = false) => ({ role: "toolResult", toolCallId: id, toolName: "lookup", content, isError, timestamp: 0 });

add("plain", "gemini-3.1-pro", { messages: [user("Say hello.")] }, {}, [ { chunks: [assistantText("Hello!")] } ]);
add("system-tools", "gemini-3.1-pro", { systemPrompt: ["First system.", "Second system."], messages: [user("Use lookup.")], tools: [lookup] }, {},
	[ { chunks: [{ response: { candidates: [{ content: { role: "model", parts: [{ functionCall: { name: "lookup", args: { term: "a" } }, thoughtSignature: "dG9vbC1zaWduYXR1cmU=" }] }, finishReason: "STOP" }], usageMetadata: { promptTokenCount: 20, candidatesTokenCount: 6, totalTokenCount: 26 } } }] } ]);
add("sampling", "gemini-3.1-pro", { messages: [user("Count.")] }, { maxTokens: 777, temperature: 0.3, topP: 0.5, thinking: { enabled: false } }, [ { chunks: [assistantText("4.")] } ]);
add("tool-history", "gemini-3.1-pro", {
	messages: [
		user("Use lookup."),
		assistant(models["gemini-3.1-pro"], [
			{ type: "thinking", thinking: "Need lookup.", thinkingSignature: "c2lnbmF0dXJlLWNhcHR1cmU=" },
			{ type: "text", text: "Looking.", textSignature: "dGV4dC1zaWduYXR1cmU=" },
			{ type: "toolCall", id: "call_capture_1", name: "lookup", arguments: { term: "a" }, thoughtSignature: "dG9vbC1zaWduYXR1cmU=" },
		]),
		result("call_capture_1", [{ type: "text", text: "Found." }]),
	],
	tools: [lookup],
}, {}, [ { chunks: [
	{ response: { candidates: [{ content: { role: "model", parts: [{ text: "Answer: ", thoughtSignature: "c2lnLTE=" }] } }] } },
	{ response: { candidates: [{ content: { role: "model", parts: [{ text: "found it." }] }, finishReason: "STOP" }], usageMetadata: { promptTokenCount: 30, candidatesTokenCount: 8, totalTokenCount: 38 } } },
] } ]);
add("tool-result-error-image", "gemini-3.1-pro", {
	messages: [
		user("Shoot."),
		assistant(models["gemini-3.1-pro"], [{ type: "toolCall", id: "call_capture_2", name: "lookup", arguments: { term: "b" } }]),
		result("call_capture_2", [{ type: "text", text: "Failed." }, { type: "image", data: png, mimeType: "image/png" }], true),
	],
	tools: [lookup],
}, {}, [ { chunks: [assistantText("Sorry.")] } ]);
add("missing-tool-result", "gemini-3.1-pro", {
	messages: [user("Use lookup."), assistant(models["gemini-3.1-pro"], [{ type: "toolCall", id: "call_capture_3", name: "lookup", arguments: { term: "c" } }]), user("Never mind.")],
	tools: [lookup],
}, {}, [ { chunks: [assistantText("Ok.")] } ]);
add("user-image", "gemini-3.1-pro", { messages: [user([{ type: "text", text: "Describe." }, { type: "image", data: png, mimeType: "image/png" }])] }, {}, [ { chunks: [assistantText("A pixel.")] } ]);
add("tool-choice-any", "gemini-3.1-pro", { messages: [user("Look it up.")], tools: [lookup] }, { toolChoice: "any" }, [ { chunks: [assistantText("Sure.")] } ]);
add("tool-choice-none", "gemini-3.1-pro", { messages: [user("Just answer.")], tools: [lookup] }, { toolChoice: "none" }, [ { chunks: [assistantText("Answer.")] } ]);
add("tool-choice-named", "gemini-3.1-pro", { messages: [user("Look it up.")], tools: [lookup] }, { toolChoice: { type: "tool", name: "lookup" } }, [ { chunks: [assistantText("Sure.")] } ]);
add("claude-beta", "claude-opus-5-5-beta", { messages: [user("Think.")] }, { reasoning: "medium" }, [ { chunks: [assistantText("Okay.")] } ]);
add("claude-thinking", "claude-opus-5-5", { messages: [user("Think.")], tools: [lookup] }, { reasoning: "high" }, [ { chunks: [
	{ response: { candidates: [{ content: { role: "model", parts: [{ text: "Considering.", thought: true }, { text: "" , thoughtSignature: "dGhvdWdodC1zaWc=" }] } }] } },
	{ response: { candidates: [{ content: { role: "model", parts: [{ text: "Done." }] }, finishReason: "STOP" }], usageMetadata: { promptTokenCount: 9, candidatesTokenCount: 4, thoughtsTokenCount: 7, totalTokenCount: 20 } } },
] } ]);
add("gemini-level", "gemini-3.7-flash", { messages: [user("Think hard.")] }, { reasoning: "high" }, [ { chunks: [assistantText("Deep.")] } ]);
add("gemini-level-minimal", "gemini-3.7-flash", { messages: [user("Think a bit.")] }, { reasoning: "minimal" }, [ { chunks: [assistantText("Shallow.")] } ]);
add("budget-model", "gemini-3.1-pro", { messages: [user("Think.")] }, { reasoning: "low" }, [ { chunks: [assistantText("Thought.")] } ]);
add("suppress-off", "gemini-3.5-flash", { messages: [user("No thinking.")] }, { thinking: { enabled: false } }, [ { chunks: [assistantText("Plain.")] } ]);
add("profiled", "gemini-3.1-pro-profiled", { messages: [user("Profile me.")] }, { reasoning: "low" }, [ { chunks: [assistantText("Profiled.")] } ]);
add("empty-retry", "gemini-3.1-pro", { messages: [user("Try again.")] }, {}, [
	{ chunks: [{ response: { candidates: [{ content: { role: "model", parts: [{ text: "" }] }, finishReason: "STOP" }], usageMetadata: { promptTokenCount: 3, candidatesTokenCount: 0, totalTokenCount: 3 } } }] },
	{ chunks: [{ response: { candidates: [{ content: { role: "model", parts: [{ text: "   " }] }, finishReason: "STOP" }], usageMetadata: { promptTokenCount: 3, candidatesTokenCount: 0, totalTokenCount: 3 } } }] },
	{ chunks: [assistantText("Third time.")] },
]);
add("in-band-error", "gemini-3.1-pro", { messages: [user("Fail.")] }, {}, [
	{ chunks: [{ error: { code: 429, message: "Resource exhausted", status: "RESOURCE_EXHAUSTED" } }] },
]);
add("http-error", "gemini-3.1-pro", { messages: [user("Reject.")] }, {}, [
	{ status: 400, body: "  invalid request  \n" },
]);
add("http-retry", "gemini-3.1-pro", { messages: [user("Recover.")] }, {}, [
	{ status: 503, body: "temporarily unavailable" },
	{ chunks: [assistantText("Recovered.")] },
]);
add("retired-tool-call", "gemini-3.1-pro", { messages: [user("Two calls.")] }, {}, [ { chunks: [
	{ response: { candidates: [{ content: { role: "model", parts: [{ functionCall: { name: "lookup", args: { term: "x" } } }] }, finishReason: "STOP" }], usageMetadata: { promptTokenCount: 5, candidatesTokenCount: 3, totalTokenCount: 8 } } },
] } ]);

const fixtures: any[] = [];
try {
	for (const scenario of scenarios) {
		captured = [];
		received = 0;
		script = scenario.script;
		uuidCounter = 0;
		const m = models[scenario.model];
		const providerSessionState = new Map();
		const stream = streamSimple(m, scenario.context, { apiKey, providerSessionState, ...scenario.options });
		let finalMessage: any;
		let error: any;
		try {
			for await (const event of stream) {
				if (event.type === "done") finalMessage = event.message;
				if (event.type === "error") error = { stopReason: event.error.stopReason, errorMessage: event.error.errorMessage?.replace(/\nraw-http-request=.*$/, "") };
			}
		} catch (err) {
			error = { thrown: String(err) };
		}
		fixtures.push({
			name: scenario.name,
			model: scenario.model,
			script: scenario.script,
			downstream: downstreamBody(scenario),
			requests: captured,
			reply: finalMessage ? { content: finalMessage.content, stopReason: finalMessage.stopReason, usage: finalMessage.usage } : null,
			error,
		});
	}
	console.log(JSON.stringify({ source: "NeoPi OWN streamGoogleGeminiCli via streamSimple (loopback Cloud Code Assist fake)", project: PROJECT, credentials, models: Object.fromEntries(Object.entries(models).map(([id, m]: any) => [id, record(m)])), fixtures }, null, 2));
} finally {
	server.stop(true);
}

// The Anthropic Messages request a client sends NeoProxy for the same
// conversation and options.
function downstreamBody(scenario: any): any {
	const body: any = { model: scenario.model === "gemini-3.1-pro-profiled" ? "gemini-3.1-pro" : scenario.model, stream: true, messages: toDownstream(scenario.context) };
	if (scenario.context.systemPrompt) body.system = scenario.context.systemPrompt.map((text: string) => ({ type: "text", text }));
	if (scenario.context.tools) body.tools = scenario.context.tools.map((tool: any) => ({ name: tool.name, description: tool.description, input_schema: tool.parameters }));
	const options = scenario.options ?? {};
	if (options.maxTokens !== undefined) body.max_tokens = options.maxTokens;
	if (options.temperature !== undefined) body.temperature = options.temperature;
	if (options.topP !== undefined) body.top_p = options.topP;
	if (options.toolChoice !== undefined) {
		body.tool_choice = typeof options.toolChoice === "string" ? { type: options.toolChoice === "any" ? "any" : options.toolChoice }
			: Array.isArray(options.toolChoice.allowedFunctionNames) ? { type: "tool", name: options.toolChoice.allowedFunctionNames[0] }
			: { type: "tool", name: options.toolChoice.name };
	}
	if (options.reasoning !== undefined) {
		body.thinking = { type: "adaptive" };
		body.output_config = { effort: options.reasoning };
	} else if (options.thinking?.enabled === false) {
		body.thinking = { type: "disabled" };
	}
	return body;
}

function toDownstream(context: any): any[] {
	const out: any[] = [];
	for (const message of context.messages) {
		if (message.role === "user" || message.role === "developer") {
			out.push({ role: "user", content: typeof message.content === "string" ? message.content : message.content.map((b: any) => b.type === "text" ? { type: "text", text: b.text } : { type: "image", source: { type: "base64", media_type: b.mimeType, data: b.data } }) });
			continue;
		}
		if (message.role === "assistant") {
			const content = message.content.map((b: any) => b.type === "text" ? { type: "text", text: b.text, ...(b.textSignature ? { textSignature: b.textSignature } : {}) } : b.type === "thinking" ? { type: "thinking", thinking: b.thinking, signature: b.thinkingSignature } : { type: "tool_use", id: b.id, name: b.name, input: b.arguments, ...(b.thoughtSignature ? { thoughtSignature: b.thoughtSignature } : {}) });
			out.push({ role: "assistant", content });
			continue;
		}
		const blocks = message.content.map((b: any) => b.type === "text" ? { type: "text", text: b.text } : { type: "image", source: { type: "base64", media_type: b.mimeType, data: b.data } });
		out.push({ role: "user", content: [{ type: "tool_result", tool_use_id: message.toolCallId, ...(message.isError ? { is_error: true } : {}), content: blocks }] });
	}
	return out;
}

// NeoPiModel-shaped record (the Go registry's embedded form).
function record(m: any) {
	return {
		id: m.id,
		name: m.name,
		api: m.api,
		provider: m.provider,
		baseUrl: "http://127.0.0.1",
		reasoning: m.reasoning,
		input: m.input,
		contextWindow: m.contextWindow ?? 0,
		maxTokens: m.maxTokens ?? 0,
		...(m.requestModelId ? { requestModelId: m.requestModelId } : {}),
		...(m.thinking
			? {
					thinking: {
						efforts: m.thinking.efforts,
						mode: m.thinking.mode,
						...(m.thinking.requiresEffort ? { requiresEffort: true } : {}),
						...(m.thinking.suppressWhenOff ? { suppressWhenOff: true } : {}),
						...(m.thinking.effortRouting ? { effortRouting: m.thinking.effortRouting } : {}),
						...(m.thinking.effortBudgets ? { effortBudgets: m.thinking.effortBudgets } : {}),
						...(m.thinking.defaultLevel ? { defaultLevel: m.thinking.defaultLevel } : {}),
					},
				}
			: {}),
		...(m.antigravityWireProfiles ? { antigravityWireProfiles: m.antigravityWireProfiles } : {}),
		geminiCompat: {
			identityClass: m.identity?.class,
			supportsFunctionPartId: m.compat?.supportsFunctionPartId === true,
			requiresSkipThoughtSignature: m.compat?.requiresSkipThoughtSignature === true,
			requiresSkipThoughtSignatureOnFirstFunctionCall: m.compat?.requiresSkipThoughtSignatureOnFirstFunctionCall === true,
			dropUnsignedThinking: m.compat?.dropUnsignedThinking === true,
			ccaLegacyParametersSchema: m.compat?.ccaLegacyParametersSchema === true,
			multimodalFunctionResponse: m.compat?.multimodalFunctionResponse === true,
			...(m.compat?.streamFirstEventTimeoutMs !== undefined ? { streamFirstEventTimeoutMs: m.compat.streamFirstEventTimeoutMs } : {}),
			flashStreamLeakWorkaround: m.compat?.flashStreamLeakWorkaround === true,
			claudeThinkingBetaHeader: m.compat?.claudeThinkingBetaHeader === true,
			antigravityClaudeToolMode: m.compat?.antigravityClaudeToolMode === true,
			...(m.compat?.antigravityUsageLabel !== undefined ? { antigravityUsageLabel: m.compat.antigravityUsageLabel } : {}),
			...(m.compat?.maxImageDimension !== undefined ? { maxImageDimension: m.compat.maxImageDimension } : {}),
			...(m.compat?.maxImagePayloadBytes !== undefined ? { maxImagePayloadBytes: m.compat.maxImagePayloadBytes } : {}),
		},
	};
}
