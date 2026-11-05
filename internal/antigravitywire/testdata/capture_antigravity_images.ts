// Runs NeoPi's OWN Antigravity image generation (pi-ai images/google-antigravity.ts
// generateAntigravityImage, with catalog discovery's fetchAntigravityImageModel)
// against a scripted loopback Cloud Code Assist. The wrapper around fetch
// records the ORIGINAL url, the headers Bun puts on the wire and the body of
// every request, so the Go port can replay each scenario byte for byte.
//
//   cd $NEOPI/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_antigravity_images.ts > antigravity_images_capture.json
process.env.PI_AI_ANTIGRAVITY_VERSION = "2.26.0";
process.env.PI_AI_ANTIGRAVITY_OS = "linux";
process.env.PI_AI_ANTIGRAVITY_ARCH = "amd64";
delete process.env.PI_AI_ANTIGRAVITY_CL;
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { generateAntigravityImage } = await import(root + "/pi-ai/src/images/google-antigravity.ts");

const nativeFetch = globalThis.fetch;
const nativeNow = Date.now;
const nativeRandom = Math.random;
const now = 1_800_000_000_000;
const randomValue = 0.123456789012345;

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
		return new Response(next.body, {
			status: next.status,
			headers: { "content-type": next.sse ? "text/event-stream" : "application/json" },
		});
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

const PRIMARY = "https://daily-cloudcode-pa.googleapis.com";
const SANDBOX = "https://daily-cloudcode-pa.sandbox.googleapis.com";
const credentials = JSON.stringify({ token: "ya29.image-token", projectId: "proj-img" });
const baseModel = { provider: "google-antigravity", id: "gemini-3-pro-image", baseUrl: PRIMARY };

const json = (value: unknown, status = 200) => ({ status, body: JSON.stringify(value) });
const text = (body: string, status: number) => ({ status, body });
const roster = (ids?: unknown) => json({ models: { "gemini-3-pro-image": { displayName: "Gemini 3 Pro Image" } }, ...(ids === undefined ? {} : { imageGenerationModelIds: ids }) });
const frame = (value: unknown) => `data: ${JSON.stringify(value)}\n\n`;
const sse = (...frames: string[]) => ({ status: 200, sse: true, body: frames.join("") });
const imageFrame = (data = "QUJD", mimeType = "image/png", usage: any = { promptTokenCount: 12, candidatesTokenCount: 1290 }) =>
	frame({ response: { candidates: [{ content: { parts: [{ inlineData: { data, mimeType } }] } }], ...(usage === null ? {} : { usageMetadata: usage }) } });
const pixel = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";

const scenarios: any[] = [
	{ name: "advertised-model", model: baseModel, request: { prompt: "a red fox" }, steps: [roster(["gemini-3.1-flash-image"]), sse(imageFrame())] },
	{ name: "no-advertised-model", model: baseModel, request: { prompt: "a red fox" }, steps: [roster(), sse(imageFrame())] },
	{ name: "request-model-id-fallback", model: { ...baseModel, requestModelId: "gemini-3-pro-image-wire" }, request: { prompt: "a red fox" }, steps: [roster([]), sse(imageFrame())] },
	{ name: "first-non-empty-advertised-id", model: baseModel, request: { prompt: "x" }, steps: [roster(["", "gemini-3-pro-image", "other"]), sse(imageFrame())] },
	{ name: "advertised-ids-not-strings", model: baseModel, request: { prompt: "x" }, steps: [roster([1, null, "gemini-3.1-flash-image"]), sse(imageFrame())] },
	{ name: "advertised-ids-not-array", model: baseModel, request: { prompt: "x" }, steps: [roster("gemini-3.1-flash-image"), sse(imageFrame())] },
	{ name: "discovery-rejected-401-falls-back", model: baseModel, request: { prompt: "x" }, steps: [text("no", 401), text("no", 401), sse(imageFrame())] },
	{ name: "discovery-rejected-403-then-transient", model: baseModel, request: { prompt: "x" }, steps: [text("no", 403), text("boom", 500), sse(imageFrame())] },
	{ name: "discovery-500-then-sandbox-advertises", model: baseModel, request: { prompt: "x" }, steps: [text("boom", 500), roster(["gemini-3.1-flash-image"]), sse(imageFrame())] },
	{ name: "discovery-invalid-json-then-ok", model: baseModel, request: { prompt: "x" }, steps: [text("{not json", 200), roster(["gemini-3.1-flash-image"]), sse(imageFrame())] },
	{ name: "discovery-empty-body-then-ok", model: baseModel, request: { prompt: "x" }, steps: [text("", 200), roster(["gemini-3.1-flash-image"]), sse(imageFrame())] },
	{ name: "discovery-scalar-root-then-ok", model: baseModel, request: { prompt: "x" }, steps: [text("7", 200), roster(["gemini-3.1-flash-image"]), sse(imageFrame())] },
	{ name: "discovery-array-root-has-no-model", model: baseModel, request: { prompt: "x" }, steps: [text("[]", 200), sse(imageFrame())] },
	{ name: "discovery-all-fail", model: baseModel, request: { prompt: "x" }, steps: [text("boom", 500), text("boom", 502), sse(imageFrame())] },
	{ name: "custom-base-url-order", model: { ...baseModel, baseUrl: "https://proxy.example.com/" }, request: { prompt: "x" }, steps: [roster(["gemini-3.1-flash-image"]), text("down", 500), text("slow", 429), sse(imageFrame())] },
	{ name: "sandbox-base-url-order", model: { ...baseModel, baseUrl: SANDBOX }, request: { prompt: "x" }, steps: [roster(["gemini-3.1-flash-image"]), text("slow", 429), sse(imageFrame())] },
	{ name: "sandbox-advertises-order", model: baseModel, request: { prompt: "x" }, steps: [text("boom", 500), roster(["gemini-3.1-flash-image"]), text("slow", 503), sse(imageFrame())] },
	{
		name: "all-options",
		model: baseModel,
		request: { prompt: "A \"quoted\" prompt with <tags> & unicode \u00e9\u2028 line", inputImages: [{ data: pixel, mimeType: "image/png" }, { data: "QUJD", mimeType: "image/jpeg" }], aspectRatio: "16:9", imageSize: "2K", count: 3 },
		steps: [roster(["gemini-3.1-flash-image"]), sse(imageFrame())],
	},
	{ name: "aspect-ratio-only", model: baseModel, request: { prompt: "x", aspectRatio: "1:1" }, steps: [roster(), sse(imageFrame())] },
	{ name: "image-size-only", model: baseModel, request: { prompt: "x", imageSize: "4K" }, steps: [roster(), sse(imageFrame())] },
	{ name: "count-zero", model: baseModel, request: { prompt: "x", count: 0 }, steps: [roster(), sse(imageFrame())] },
	{ name: "retry-429-then-ok", model: baseModel, request: { prompt: "x" }, steps: [roster(), text(JSON.stringify({ error: { message: "quota" } }), 429), sse(imageFrame())] },
	{ name: "retry-500-then-ok", model: baseModel, request: { prompt: "x" }, steps: [roster(), text("boom", 500), sse(imageFrame())] },
	{ name: "all-endpoints-500", model: baseModel, request: { prompt: "x" }, steps: [roster(), text("one", 500), text(JSON.stringify({ error: { message: "two" } }), 503)] },
	{ name: "all-endpoints-429", model: baseModel, request: { prompt: "x" }, steps: [roster(), text("one", 429), text(JSON.stringify({ detail: "slow down" }), 429)] },
	{ name: "non-retryable-400-error-message", model: baseModel, request: { prompt: "x" }, steps: [roster(), text(JSON.stringify({ error: { message: "Request contains an invalid argument." } }), 400)] },
	{ name: "non-retryable-400-detail", model: baseModel, request: { prompt: "x" }, steps: [roster(), text(JSON.stringify({ detail: "bad", error: { message: "ignored" } }), 400)] },
	{ name: "non-retryable-403-plain", model: baseModel, request: { prompt: "x" }, steps: [roster(), text("forbidden", 403)] },
	{ name: "non-retryable-404-json-without-message", model: baseModel, request: { prompt: "x" }, steps: [roster(), text(JSON.stringify({ error: { code: 404 } }), 404)] },
	{ name: "non-retryable-401", model: baseModel, request: { prompt: "x" }, steps: [roster(), text(JSON.stringify({ error: { message: "expired" } }), 401)] },
	{
		name: "multi-chunk-text-and-usage",
		model: baseModel,
		request: { prompt: "x" },
		steps: [
			roster(),
			sse(
				frame({ response: { candidates: [{ content: { parts: [{ text: "Here you go" }, { inlineData: { data: "QUFB", mimeType: "image/png" } }] } }, { content: { parts: [{ text: "second" }, { inlineData: { data: "QkJC" } }, { inlineData: { mimeType: "image/png" } }, { text: "" }] } }], usageMetadata: { promptTokenCount: 1, candidatesTokenCount: 2 } } }),
				frame({ response: { candidates: [{ content: { parts: [{ text: "  tail  " }, { inlineData: { data: "Q0ND", mimeType: "image/webp" } }] } }] } }),
				frame({ response: { candidates: [{}] , usageMetadata: { promptTokenCount: 7, candidatesTokenCount: 9, totalTokenCount: 99 } } }),
				frame({ traceId: "no-response" }),
				frame({ response: {} }),
			),
		],
	},
	{ name: "usage-object-resets", model: baseModel, request: { prompt: "x" }, steps: [roster(), sse(imageFrame("QUJD", "image/png", { promptTokenCount: 5, candidatesTokenCount: 6 }), frame({ response: { usageMetadata: {} } }))] },
	{ name: "usage-non-numbers", model: baseModel, request: { prompt: "x" }, steps: [roster(), sse(imageFrame("QUJD", "image/png", { promptTokenCount: "5", candidatesTokenCount: 6.5 }))] },
	{ name: "empty-stream", model: baseModel, request: { prompt: "x" }, steps: [roster(), sse()] },
	{ name: "text-only-stream", model: baseModel, request: { prompt: "x" }, steps: [roster(), sse(frame({ response: { candidates: [{ content: { parts: [{ text: "I cannot draw that." }] } }] } }))] },
	{ name: "done-sentinel-stops", model: baseModel, request: { prompt: "x" }, steps: [roster(), sse(imageFrame("QUFB"), "data: [DONE]\n\n", imageFrame("QkJC"))] },
	{ name: "comment-and-event-lines", model: baseModel, request: { prompt: "x" }, steps: [roster(), { status: 200, sse: true, body: `: keepalive\n\nevent: message\nid: 3\nretry: 100\ndata: ${JSON.stringify({ response: { candidates: [{ content: { parts: [{ inlineData: { data: "QUJD", mimeType: "image/png" } }] } }] } })}\n\n` }] },
	{ name: "malformed-event", model: baseModel, request: { prompt: "x" }, steps: [roster(), sse("data: {oops}\n\n")] },
	{ name: "truncated-tail", model: baseModel, request: { prompt: "x" }, steps: [roster(), { status: 200, sse: true, body: imageFrame() + 'data: {"response":{"candidates":[{"content":' }] },
	{ name: "invalid-input-base64", model: baseModel, request: { prompt: "x", inputImages: [{ data: "%%%", mimeType: "image/png" }] }, steps: [] },
	{ name: "empty-input-data", model: baseModel, request: { prompt: "x", inputImages: [{ data: "", mimeType: "image/png" }] }, steps: [] },
	{ name: "bad-trailing-bits", model: baseModel, request: { prompt: "x", inputImages: [{ data: "QUJ=", mimeType: "image/png" }] }, steps: [] },
	{ name: "bad-padding", model: baseModel, request: { prompt: "x", inputImages: [{ data: "QUJDRA===", mimeType: "image/png" }] }, steps: [] },
	{ name: "unpadded-input-accepted", model: baseModel, request: { prompt: "x", inputImages: [{ data: "QUJDRA", mimeType: "IMAGE/PNG" }] }, steps: [roster(), sse(imageFrame())] },
	{ name: "invalid-input-mime", model: baseModel, request: { prompt: "x", inputImages: [{ data: pixel, mimeType: "text/plain" }] }, steps: [] },
	{ name: "input-mime-with-hash", model: baseModel, request: { prompt: "x", inputImages: [{ data: pixel, mimeType: "image/x#y" }] }, steps: [roster(), sse(imageFrame())] },
	{ name: "second-input-invalid", model: baseModel, request: { prompt: "x", inputImages: [{ data: pixel, mimeType: "image/png" }, { data: "***", mimeType: "image/png" }] }, steps: [] },
];

const results: any[] = [];
try {
	for (const scenario of scenarios) {
		script = [...scenario.steps];
		requests = [];
		Date.now = () => now;
		Math.random = () => randomValue;
		let result: any = null;
		let error: any = null;
		try {
			result = await generateAntigravityImage(scenario.model, scenario.request, { apiKey: credentials, fetch: captureFetch });
		} catch (cause: any) {
			error = { name: cause?.name ?? null, message: String(cause?.message ?? cause), status: cause?.status ?? null };
		}
		Date.now = nativeNow;
		Math.random = nativeRandom;
		results.push({ name: scenario.name, model: scenario.model, request: scenario.request, steps: scenario.steps, requests, result, error });
	}
} finally {
	Date.now = nativeNow;
	Math.random = nativeRandom;
	server.stop(true);
}
console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN generateAntigravityImage (images/google-antigravity.ts) with fetchAntigravityImageModel",
			now,
			randomValue,
			randomId: randomValue.toString(36).slice(2, 11),
			credentials: JSON.parse(credentials),
			env: { PI_AI_ANTIGRAVITY_VERSION: "2.26.0", PI_AI_ANTIGRAVITY_OS: "linux", PI_AI_ANTIGRAVITY_ARCH: "amd64" },
			scenarios: results,
		},
		null,
		1,
	),
);
