// Captures NeoPi's own thinking markup healer, thinking-fence stripper, and
// demoted-thinking renderer. Run from the NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks /path/to/internal/neopidialect/testdata/capture_thinking_healing.ts \
//     > /path/to/internal/neopidialect/testdata/thinking_healing.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { StreamMarkupHealing } = await import(root + "/pi-ai/src/utils/stream-markup-healing.ts");
const { ThinkingFenceStripper } = await import(root + "/pi-ai/src/dialect/thinking-fence-strip.ts");
const { renderDemotedThinking } = await import(root + "/pi-ai/src/dialect/demotion.ts");

type Event = { kind: "text" | "thinking"; text: string };
type HealingCase = { name: string; chunks: string[]; feeds: Event[][]; flush: Event[] };
type StripCase = { name: string; chunks: string[]; pushes: string[]; flush: string };

const sourceInputs: Array<[string, string]> = [
	["plain", "plain visible answer"],
	["plain-unicode", "visible café 🧠 text"],
	["empty", ""],
	["think-simple", "before<think>reasoning here</think>after"],
	["thinking-simple", "before<thinking>reasoning here</thinking>after"],
	["scratchpad", "before<scratchpad>private note</scratchpad>after"],
	["unterminated-think", "before<think>unterminated thought"],
	["unterminated-thinking", "before<thinking>unterminated thought"],
	["empty-thinking", "<think></think>"],
	["nested-tag", "<think>outer <think>inner</think> tail</think>visible"],
	["odd-close", "</think> remains visible"],
	["code-inline", "literal `<think>not markup</think>` then <think>yes</think>"],
	["code-fence", "```\n<think>not markup</think>\n```\n<think>yes</think>"],
	["code-fence-indented", "  ```lang\n<thinking>not markup</thinking>\n  ```\n<think>yes</think>"],
	["trailing-backtick", "answer `"],
	["gemini-fence-3", "before```thinking\nthought\n```answer"],
	["gemini-fence-4", "before````thinking\nthought\n````answer"],
	["gemini-fence-5", "before`````thinking\nthought\n`````answer"],
	["gemini-fence-6", "before``````thinking\nthought\n``````answer"],
	["gemini-fence-crlf", "before```thinking\r\nthought\r\n```answer"],
	["gemini-unclosed", "before```thinking\nthought still going"],
	["gemini-inline-close", "```thinking\nthought```answer"],
	["gemini-nested-code", "```thinking\nreasoning\n```rs\nlet x = 1;\n```\nstill reasoning\n```answer"],
	["gemini-nested-long", "```thinking\nreasoning\n````go\nlet x = 1;\n````\nstill reasoning\n```answer"],
	["gemini-nested-tilde", "```thinking\nreasoning\n~~~rs\ncode\n~~~\nstill reasoning\n```answer"],
	["gemini-nested-unclosed", "```thinking\nreasoning\n```rs\ncode without nested close"],
	["gemma-channel", "visible<|channel>thought\nreasoning<channel|>answer"],
	["harmony-rendered", "visible<|start|>assistant<|channel|>analysis<|message|>reasoning<|end|>answer"],
	["harmony-bare", "visible<|channel|>analysis<|message|>reasoning<|end|>answer"],
	["unicode-inside", "before<think>emoji 🧠 café</think>after"],
	["unicode-around", "🧠before<thinking>café</thinking>🧠after"],
	["crlf", "first\r\n<think>inner\r\n</think>\r\nlast"],
	["near-tag", "<thinkingx>visible</thinkingx> and <thinker>plain"],
	["tag-adjacent", "a<think>x</think><thinking>y</thinking>z"],
	["harmony-unterminated", "<|channel|>analysis<|message|>thought without end"],
	["gemma-unterminated", "<|channel>thought\nthought without end"],
	["multiple-blocks", "<think>one</think> middle <think>two</think> end"],
]


function fixedChunks(text: string, widths: number[]): string[] {
	const chars = Array.from(text);
	const chunks: string[] = [];
	let offset = 0;
	let i = 0;
	while (offset < chars.length) {
		const end = Math.min(chars.length, offset + widths[i % widths.length]!);
		chunks.push(chars.slice(offset, end).join(""));
		offset = end;
		i++;
	}
	return chunks;
}

const healingCases: HealingCase[] = [];
function captureHealing(name: string, chunks: string[]): void {
	const healer = new StreamMarkupHealing({ pattern: "thinking" });
	const feeds = chunks.map(chunk => healer.feedEvents(chunk).map(event => {
		if (event.type === "text") return { kind: "text" as const, text: event.text };
		if (event.type === "thinking") return { kind: "thinking" as const, text: event.thinking };
		throw new Error(`unexpected tool event in thinking-only healer: ${JSON.stringify(event)}`);
	}));
	const flush = healer.flushEvents().map(event => {
		if (event.type === "text") return { kind: "text" as const, text: event.text };
		if (event.type === "thinking") return { kind: "thinking" as const, text: event.thinking };
		throw new Error(`unexpected tool event during flush: ${JSON.stringify(event)}`);
	});
	healingCases.push({ name, chunks, feeds, flush });
}

const markupDelimiters = [
	"<think>", "</think>", "<thinking>", "</thinking>", "<scratchpad>", "</scratchpad>",
	"```thinking\n", "```", "<|channel>thought\n", "<channel|>",
	"<|start|>assistant<|channel|>analysis<|message|>", "<|channel|>analysis<|message|>", "<|end|>",
];
for (const [name, text] of sourceInputs) {
	captureHealing(`${name}:whole`, [text]);
	captureHealing(`${name}:unit-chunks`, Array.from(text));
	captureHealing(`${name}:empty-interleave`, ["", ...Array.from(text).flatMap(part => [part, ""]), ""]);
	captureHealing(`${name}:fixed-1-2-3`, fixedChunks(text, [1, 2, 3]));
	// Split every markup delimiter at every UTF-16 boundary. Every delimiter is
	// ASCII, so this also covers every byte boundary relevant to the Go scanner.
	for (const delimiter of markupDelimiters) {
		for (let start = text.indexOf(delimiter); start !== -1; start = text.indexOf(delimiter, start + 1)) {
			for (let offset = 1; offset < delimiter.length; offset++) {
				const split = start + offset;
				captureHealing(`${name}:split-${delimiter.replaceAll("\n", "\\n")}-${offset}`, [
					text.slice(0, split),
					text.slice(split),
				]);
			}
		}
	}
}
// Keep the non-BMP code point valid UTF-8 in the JSON fixture while placing it
// between separate streamed chunks.
captureHealing("non-bmp-split-around-markup", ["before<think>", "🧠", "</think>after"]);
// Additional tiny oddities exercise partial delimiters at stream end.
for (const [name, text] of [
	["tag-prefix-at-eof", "visible<thi"],
	["close-prefix-at-eof", "<think>thought</thi"],
	["fence-prefix-at-eof", "visible```think"],
	["fence-close-prefix-at-eof", "```thinking\nthought\n``"],
	["bare-close-after-text", "visible</think>"],
	["harmony-end-prefix", "<|channel|>analysis<|message|>thought<|en"],
	["gemma-close-prefix", "<|channel>thought\nthought<channel"],
] as const) captureHealing(`${name}:chunked`, fixedChunks(text, [2, 1, 4]));

const stripperInputs: Array<[string, string]> = [
	["three-ticks", "```thinking\nthought\n```reasoning\nmore"],
	["four-ticks", "  ````THINKING  \nreason\n"],
	["reasoning-crlf", "```reasoning\r\nbody\r\n"],
	["inline", "prefix ```thinking suffix\n"],
	["language-fence", "```rust\ncode\n```\n"],
	["bare-fence", "```\nbody\n```"],
	["overindented", "    ```thinking\nbody"],
	["less-than-three", "  ``thinking\nbody"],
	["partial-opener", "  ````think"],
	["unicode", "🧠\n```reasoning\ntext"],
	["cr-split", "```thinking\r\nrest"],
];
const stripperCases: StripCase[] = [];
function captureStripper(name: string, chunks: string[]): void {
	const stripper = new ThinkingFenceStripper();
	const pushes = chunks.map(chunk => stripper.push(chunk));
	stripperCases.push({ name, chunks, pushes, flush: stripper.flush() });
}
const stripperDelimiters = ["```thinking", "```reasoning", "````THINKING"];
for (const [name, text] of stripperInputs) {
	captureStripper(name, fixedChunks(text, [1, 2, 3, 5]));
	// Split each standalone opener at every boundary to exercise its streaming prefix hold.
	for (const delimiter of stripperDelimiters) {
		for (let start = text.indexOf(delimiter); start !== -1; start = text.indexOf(delimiter, start + 1)) {
			for (let offset = 1; offset < delimiter.length; offset++) {
				const split = start + offset;
				captureStripper(`${name}:split-${delimiter}-${offset}`, [text.slice(0, split), text.slice(split)]);
			}
		}
	}
}

const demotionModels = [
	"claude-3-7-sonnet-latest", "anthropic/claude-opus-4", "gemini-2.5-pro", "google/gemma-3-27b-it",
	"gpt-4.1", "gpt-oss-120b", "deepseek-r1", "glm-4.5", "kimi-k2", "qwen3-235b-a22b",
	"minimax-m2", "unknown-model",
];
const demotions = demotionModels.flatMap(model => ["reasoning", "<thinking>inner</thinking>", "<think>outer <think>nested</think> end</think>"].map(text => ({
	model,
	text,
	output: renderDemotedThinking(model, text),
})));

console.log(JSON.stringify({
	source: "NeoPi OWN StreamMarkupHealing(thinking), ThinkingFenceStripper, renderDemotedThinking",
	caseCount: healingCases.length,
	cases: healingCases,
	strippers: stripperCases,
	demotions: demotions,
}, null, 2));
