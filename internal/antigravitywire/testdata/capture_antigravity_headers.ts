// Runs NeoPi's OWN Antigravity client identity (pi-catalog wire/gemini-headers.ts):
// the update-manifest parser, the user agent under every env override, and
// ensureAntigravityVersion against scripted manifest answers.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file> > antigravity_headers_capture.json
//
// ensureAntigravityVersion keeps process-wide state, so every scenario loads a
// fresh copy of the module (a distinct query string is a distinct module).
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const modulePath = root + "/pi-catalog/src/wire/gemini-headers.ts";
const OVERRIDES = ["PI_AI_ANTIGRAVITY_VERSION", "PI_AI_ANTIGRAVITY_CL", "PI_AI_ANTIGRAVITY_OS", "PI_AI_ANTIGRAVITY_ARCH"];
const clearEnv = () => {
	for (const key of OVERRIDES) delete process.env[key];
};
let instance = 0;
const load = async () => await import(`${modulePath}?instance=${instance++}`);

// ---- manifest parser ----------------------------------------------------------
const MANIFESTS = [
	"version: 2.31.0\npath: Antigravity.dmg\n",
	"files:\n  - url: a.zip\nversion: 3.0.1\n",
	'version: "2.27.4"\n',
	"version: '2.27.5'\n",
	"version: 2.28.0 # latest\n",
	"  version  :  2.29.0  \n",
	"version: 2.28\n",
	"version: v2.28.0\n",
	"version: 2.28.0-beta.1\n",
	"version: \n",
	"name: x\npath: y\n",
	"",
	"version: 1.2.3\nversion: 4.5.6\n",
	"version: 2.30.0\r\npath: x\r\n",
	"# version: 2.30.0\n",
	"versions: 2.30.0\n",
	'version: ""\n',
];
const mod = await load();
const manifests = MANIFESTS.map(yaml => ({ yaml, version: mod.parseAntigravityManifestVersion(yaml) }));

// ---- user agent ---------------------------------------------------------------
const ENV_CASES: Record<string, string>[] = [
	{ PI_AI_ANTIGRAVITY_VERSION: "2.26.0", PI_AI_ANTIGRAVITY_OS: "linux", PI_AI_ANTIGRAVITY_ARCH: "amd64" },
	{ PI_AI_ANTIGRAVITY_VERSION: "9.9.9", PI_AI_ANTIGRAVITY_OS: "darwin", PI_AI_ANTIGRAVITY_ARCH: "arm64", PI_AI_ANTIGRAVITY_CL: "42" },
	{ PI_AI_ANTIGRAVITY_VERSION: "2.26.0", PI_AI_ANTIGRAVITY_OS: "windows", PI_AI_ANTIGRAVITY_ARCH: "amd64" },
	{ PI_AI_ANTIGRAVITY_VERSION: "2.26.0", PI_AI_ANTIGRAVITY_CL: "7" },
	{ PI_AI_ANTIGRAVITY_VERSION: "2.26.0", PI_AI_ANTIGRAVITY_OS: "", PI_AI_ANTIGRAVITY_ARCH: "" },
	{},
];
const userAgents = ENV_CASES.map(env => {
	clearEnv();
	Object.assign(process.env, env);
	return { env, userAgent: mod.getAntigravityUserAgent() };
});
clearEnv();

// ---- ensureAntigravityVersion -------------------------------------------------
type Answer = { status?: number; body?: string; throw?: true };
const ENSURE: { name: string; env?: Record<string, string>; answers: Answer[] }[] = [
	{ name: "manifest answers", answers: [{ body: "version: 2.31.0\npath: Antigravity.dmg\n" }] },
	{ name: "manifest 500 then 500", answers: [{ status: 500 }, { status: 500 }] },
	{ name: "manifest network error", answers: [{ throw: true }, { throw: true }] },
	{ name: "manifest without version", answers: [{ body: "name: x\n" }, { body: "version: 2.31.0\n" }] },
	{ name: "manifest invalid version", answers: [{ body: "version: 2.31\n" }, { body: "version: 2.31.0\n" }] },
	{ name: "env override skips lookup", env: { PI_AI_ANTIGRAVITY_VERSION: "5.5.5" }, answers: [{ body: "version: 2.31.0\n" }] },
];
const ensure = [];
for (const scenario of ENSURE) {
	clearEnv();
	Object.assign(process.env, scenario.env ?? {}, { PI_AI_ANTIGRAVITY_OS: "linux", PI_AI_ANTIGRAVITY_ARCH: "amd64" });
	const fresh = await load();
	const requests: { url: string; cacheControl?: string; userAgent?: string }[] = [];
	const stub = Object.assign(
		async (input: any, init: any) => {
			const headers = Object.fromEntries(
				Object.entries(init?.headers ?? {}).map(([key, value]) => [key.toLowerCase(), String(value)]),
			);
			requests.push({ url: String(input), cacheControl: headers["cache-control"], userAgent: headers["user-agent"] });
			const answer = scenario.answers[requests.length - 1] ?? { status: 599 };
			if (answer.throw) throw new Error("network down");
			return new Response(answer.body ?? "", { status: answer.status ?? 200 });
		},
		{ preconnect: () => {} },
	);
	await fresh.ensureAntigravityVersion(stub);
	const afterFirst = { requests: requests.length, version: fresh.getAntigravityVersion(), userAgent: fresh.getAntigravityUserAgent() };
	await fresh.ensureAntigravityVersion(stub);
	const afterSecond = { requests: requests.length, version: fresh.getAntigravityVersion() };
	ensure.push({ name: scenario.name, env: scenario.env ?? {}, requests, afterFirst, afterSecond });
}
clearEnv();

console.log(
	JSON.stringify(
		{ source: "NeoPi OWN pi-catalog wire/gemini-headers.ts", hostPlatform: process.platform, manifests, userAgents, ensure },
		null,
		1,
	),
);
