// Runs NeoPi's OWN parseCallbackInput (registry/oauth/callback-server.ts), the
// parser behind the manual-paste half of every oauth-code login, over pasted
// redirect URLs, query strings and bare codes.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { parseCallbackInput } = await import(root + "/pi-ai/src/registry/oauth/callback-server.ts");

const inputs = [
	"http://127.0.0.1:51121/oauth-callback?code=4%2F0Acode&state=abc123",
	"http://127.0.0.1:51121/oauth-callback?code=4/0Acode",
	"http://127.0.0.1:51121/oauth-callback?state=abc123",
	"http://127.0.0.1:51121/oauth-callback?error=access_denied&state=abc123",
	"http://127.0.0.1:51121/oauth-callback?code=a+b%20c&code=second&state=s",
	"  http://localhost:1/oauth-callback?code=padded&state=s  ",
	"https://example.com/?code=&state=s",
	"code=4%2F0Acode&state=abc123",
	"?code=lead&state=q",
	"#code=frag&state=f",
	"code=only",
	"state=only&code=reordered",
	"code=a;b&state=semi",
	"4/0AbareCode",
	"4/0AbareCode#state123",
	"a#b#c",
	"#onlystate",
	"4/0Acode#",
	"",
	"   ",
	"abc:def",
	"foo://bar?code=custom&state=x",
	"4/0A_-bare",
	"ya29.not-a-code with spaces",
];
console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN parseCallbackInput",
			cases: inputs.map(input => {
				const parsed = parseCallbackInput(input);
				return { input, code: parsed.code ?? null, state: parsed.state ?? null, hasCode: "code" in parsed, hasState: "state" in parsed };
			}),
		},
		null,
		2,
	),
);
