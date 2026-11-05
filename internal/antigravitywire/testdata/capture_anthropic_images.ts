// Runs NeoPi's OWN prepareAnthropicImageContext (the Claude image clamp shared
// by anthropic-messages and the Cloud Code Assist wire) over generated PNG /
// JPEG images and records, per image block, whether it changed plus the output
// mime type, decoded dimensions and base64 length. The generated inputs are
// written into the capture so the Go port replays exactly the same bytes.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file> > anthropic_images_capture.json
import { deflateSync } from "node:zlib";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { prepareAnthropicImageContext } = await import(root + "/pi-ai/src/providers/anthropic-images.ts");
const BunImage = (Bun as any).Image;

// ---- deterministic generators ------------------------------------------------

function crc32(bytes: Uint8Array): number {
	const table: number[] = [];
	for (let n = 0; n < 256; n++) {
		let c = n;
		for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
		table[n] = c >>> 0;
	}
	let crc = 0xffffffff;
	for (const byte of bytes) crc = table[(crc ^ byte) & 255] ^ (crc >>> 8);
	return (crc ^ 0xffffffff) >>> 0;
}

function chunk(type: string, data: Uint8Array): Uint8Array {
	const out = new Uint8Array(12 + data.length);
	const view = new DataView(out.buffer);
	view.setUint32(0, data.length);
	for (let i = 0; i < 4; i++) out[4 + i] = type.charCodeAt(i);
	out.set(data, 8);
	view.setUint32(8 + data.length, crc32(out.subarray(4, 8 + data.length)));
	return out;
}

type Pixel = [number, number, number];

/** 8-bit RGB PNG; `padding` appends a tEXt chunk of that many bytes. */
function encodePng(width: number, height: number, pixel: (x: number, y: number) => Pixel, padding = 0): Buffer {
	const raw = new Uint8Array(height * (1 + width * 3));
	let p = 0;
	for (let y = 0; y < height; y++) {
		raw[p++] = 0;
		for (let x = 0; x < width; x++) {
			const c = pixel(x, y);
			raw[p++] = c[0];
			raw[p++] = c[1];
			raw[p++] = c[2];
		}
	}
	const ihdr = new Uint8Array(13);
	const view = new DataView(ihdr.buffer);
	view.setUint32(0, width);
	view.setUint32(4, height);
	ihdr[8] = 8;
	ihdr[9] = 2;
	const parts = [new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ihdr)];
	if (padding > 0) {
		// Incompressible-looking but deterministic filler for the tEXt chunk.
		const text = new Uint8Array(padding);
		text.set(new TextEncoder().encode("Comment\0"));
		let s = 12345;
		for (let i = 8; i < padding; i++) {
			s = (s * 1103515245 + 12345) & 0x7fffffff;
			text[i] = 65 + (s % 26);
		}
		parts.push(chunk("tEXt", text));
	}
	parts.push(chunk("IDAT", deflateSync(raw)), chunk("IEND", new Uint8Array()));
	return Buffer.concat(parts);
}

function prng(seed: number): () => number {
	let s = seed >>> 0;
	return () => {
		s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
		return s >>> 24;
	};
}

/** Flat colour bands: PNG is tiny, JPEG cannot beat it. */
const bands = (x: number, y: number): Pixel => {
	const band = Math.floor(x / 40) % 4;
	return band === 0 ? [200, 30, 30] : band === 1 ? [30, 200, 30] : band === 2 ? [30, 30, 200] : [240, 240, 240];
};

/** Per-pixel noise: PNG cannot compress it, JPEG q85 beats it. */
function noise(seed: number): (x: number, y: number) => Pixel {
	const next = prng(seed);
	return () => [next(), next(), next()];
}

async function toJpeg(png: Buffer): Promise<Buffer> {
	return Buffer.from(await new BunImage(png).jpeg({ quality: 85 }).bytes());
}

const assets: Record<string, { data: string; mimeType: string }> = {};
function addPng(name: string, buf: Buffer) {
	assets[name] = { data: buf.toString("base64"), mimeType: "image/png" };
}
async function addJpeg(name: string, png: Buffer) {
	assets[name] = { data: (await toJpeg(png)).toString("base64"), mimeType: "image/jpeg" };
}

// Small, within every cap.
addPng("small-png", encodePng(100, 40, bands));
await addJpeg("small-jpeg", encodePng(100, 40, bands));
// Dimension boundary: 8000 stays, 8001 clamps.
addPng("strip-8000x1-png", encodePng(8000, 1, bands));
addPng("strip-8001x1-png", encodePng(8001, 1, bands));
await addJpeg("strip-8001x1-jpeg", encodePng(8001, 1, bands));
// Over the cap in one axis with a non-trivial other axis.
addPng("strip-9000x30-png", encodePng(9000, 30, bands));
await addJpeg("strip-9000x30-jpeg", encodePng(9000, 30, bands));
addPng("tall-30x9000-png", encodePng(30, 9000, (x, y) => bands(y, x)));
// Many-image cap (2000) probes.
addPng("strip-2000x4-png", encodePng(2000, 4, bands));
addPng("strip-2001x2-png", encodePng(2001, 2, bands));
addPng("strip-3000x10-png", encodePng(3000, 10, bands));
// Custom model dimension cap (500) probes.
addPng("strip-600x20-png", encodePng(600, 20, bands));
// Noise images: PNG loses to JPEG.
addPng("noise-64x64-png", encodePng(64, 64, noise(7)));
addPng("noise-128x128-png", encodePng(128, 128, noise(11)));
// Bloated 1x1 PNG: a re-encode is strictly smaller.
addPng("bloated-1x1-png", encodePng(1, 1, () => [10, 20, 30], 3000));
// Not an image at all.
assets["garbage"] = { data: Buffer.from("this is not an image").toString("base64"), mimeType: "image/png" };

// ---- cases -------------------------------------------------------------------

type Block = { type: "text"; text: string } | { type: "image"; asset: string; url?: string; providerFile?: unknown };
type Message = { role: "user" | "developer" | "toolResult" | "assistant"; content: string | Block[] };
interface Case {
	name: string;
	/** exact: dims and mime are decisive and asserted byte-for-decision; otherwise the Go port checks bounds only. */
	exact: boolean;
	compat: { maxImageDimension?: number; maxImagePayloadBytes?: number };
	messages: Message[];
}

const img = (asset: string, extra: Partial<Block> = {}): Block => ({ type: "image", asset, ...extra }) as Block;
const text = (t: string): Block => ({ type: "text", text: t });
const user = (...content: Block[]): Message => ({ role: "user", content });

const many = (asset: string, n: number): Block[] => Array.from({ length: n }, () => img(asset));

const cases: Case[] = [
	{ name: "within-bounds", exact: true, compat: {}, messages: [user(text("look"), img("small-png"), img("small-jpeg"))] },
	{ name: "at-8000-boundary", exact: true, compat: {}, messages: [user(img("strip-8000x1-png"))] },
	{ name: "over-8000-png", exact: true, compat: {}, messages: [user(img("strip-8001x1-png"))] },
	{ name: "over-8000-jpeg-source", exact: true, compat: {}, messages: [user(img("strip-8001x1-jpeg"))] },
	{ name: "wide-9000x30", exact: true, compat: {}, messages: [user(img("strip-9000x30-png"), img("strip-9000x30-jpeg"))] },
	{ name: "tall-30x9000", exact: true, compat: {}, messages: [user(img("tall-30x9000-png"))] },
	{
		name: "mixed-roles-and-blocks",
		exact: true,
		compat: {},
		messages: [
			{ role: "developer", content: [text("dev"), img("strip-8001x1-png")] },
			{ role: "user", content: "plain string content" },
			{ role: "assistant", content: [text("assistant text")] },
			{ role: "toolResult", content: [text("tool output"), img("strip-9000x30-png"), img("small-png")] },
			user(text("tail"), img("small-jpeg")),
		],
	},
	{
		name: "url-and-provider-file-dropped",
		exact: true,
		compat: {},
		messages: [user(img("strip-8001x1-png", { url: "https://blob.example/x.png", providerFile: { id: "f1" } }), img("small-png", { url: "https://blob.example/y.png" }))],
	},
	{
		name: "twenty-images-keep-8000",
		exact: true,
		compat: {},
		messages: [user(...many("small-png", 19), img("strip-3000x10-png"))],
	},
	{
		name: "twenty-one-images-cap-2000",
		exact: true,
		compat: {},
		messages: [user(...many("small-png", 17), img("strip-3000x10-png"), img("strip-2001x2-png"), img("strip-2000x4-png"), img("strip-8001x1-png"))],
	},
	{
		name: "many-images-across-messages",
		exact: true,
		compat: {},
		messages: [
			user(...many("small-jpeg", 8)),
			{ role: "toolResult", content: [...many("small-png", 8), img("strip-3000x10-png")] },
			{ role: "developer", content: [...many("small-png", 4), img("strip-2001x2-png")] },
		],
	},
	{
		name: "many-images-with-low-host-cap",
		exact: true,
		compat: { maxImageDimension: 500 },
		messages: [user(...many("small-png", 21), img("strip-600x20-png"))],
	},
	{ name: "custom-dimension-500", exact: true, compat: { maxImageDimension: 500 }, messages: [user(img("strip-600x20-png"), img("small-png"))] },
	{
		name: "custom-dimension-above-default-raises-cap",
		exact: true,
		compat: { maxImageDimension: 12000 },
		messages: [user(img("strip-8001x1-png"), img("strip-9000x30-png"))],
	},
	{
		// Over the byte budget, inside the pixel cap, one re-encode attempt is enough.
		name: "payload-over-budget-single-pass",
		exact: true,
		compat: { maxImagePayloadBytes: 12000 },
		messages: [user(img("noise-64x64-png"), img("small-png"))],
	},
	{
		// Several shrink attempts: dimensions depend on encoder output sizes, so the Go port checks bounds only.
		name: "payload-over-budget-multi-pass",
		exact: false,
		compat: { maxImagePayloadBytes: 3400 },
		messages: [user(img("noise-128x128-png"))],
	},
	{
		// Within the pixel cap but the payload budget is tiny; the bloated PNG shrinks to 1x1 png.
		name: "bloated-1x1-shrinks",
		exact: true,
		compat: { maxImagePayloadBytes: 400 },
		messages: [user(img("bloated-1x1-png"))],
	},
	{ name: "undecodable-untouched", exact: true, compat: { maxImageDimension: 10 }, messages: [user(img("garbage"), img("small-png"))] },
];

// ---- run ---------------------------------------------------------------------

const model = (compat: Case["compat"]) => ({ input: ["text", "image"], compat });

function materialize(message: Message): any {
	if (typeof message.content === "string") return { ...message };
	return {
		...message,
		content: message.content.map(block =>
			block.type === "image"
				? { type: "image", data: assets[block.asset].data, mimeType: assets[block.asset].mimeType, ...(block.url ? { url: block.url } : {}), ...(block.providerFile ? { providerFile: block.providerFile } : {}) }
				: block,
		),
	};
}

function isImageCarrier(role: string) {
	return role === "user" || role === "developer" || role === "toolResult";
}

const out = [];
for (const c of cases) {
	const messages = c.messages.map(materialize);
	const context = { messages } as any;
	const result = await prepareAnthropicImageContext(context, model(c.compat) as any);
	const imageCount = messages.reduce(
		(n: number, m: any) => n + (isImageCarrier(m.role) && Array.isArray(m.content) ? m.content.filter((b: any) => b.type === "image").length : 0),
		0,
	);
	const records: any[] = [];
	for (let mi = 0; mi < messages.length; mi++) {
		const before = messages[mi];
		const after = result.messages[mi];
		if (!isImageCarrier(before.role) || !Array.isArray(before.content)) continue;
		for (let bi = 0; bi < before.content.length; bi++) {
			const b = before.content[bi];
			if (b.type !== "image") continue;
			const a = after.content[bi];
			// Undecodable payloads have no metadata; they must come back untouched.
			const meta = await new BunImage(Buffer.from(a.data, "base64")).metadata().catch(() => ({ width: 0, height: 0 }));
			records.push({
				message: mi,
				block: bi,
				changed: a.data !== b.data || a.mimeType !== b.mimeType,
				mimeType: a.mimeType,
				width: meta.width,
				height: meta.height,
				base64Length: a.data.length,
				hasUrl: "url" in a,
				hasProviderFile: "providerFile" in a,
			});
		}
	}
	const hostMax = c.compat.maxImageDimension ?? 8000;
	out.push({
		name: c.name,
		exact: c.exact,
		compat: c.compat,
		imageCount,
		maxDimension: imageCount > 20 ? Math.min(2000, hostMax) : hostMax,
		payloadBudget: Math.floor((c.compat.maxImagePayloadBytes ?? 10 * 1024 * 1024) * 0.9),
		unchangedContext: result === context,
		messages: c.messages,
		images: records,
	});
}

console.log(JSON.stringify({ source: "NeoPi OWN prepareAnthropicImageContext", bun: Bun.version, assets, cases: out }));
