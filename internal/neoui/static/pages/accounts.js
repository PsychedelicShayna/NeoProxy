// Account owners never use the Engine management endpoints. The server scopes
// inventory, meter configuration, login completion, key scopes and statistics.
import { h, fmtCompact, fmtAgo } from "../lib/dom.js";
import { neo, engine } from "../lib/client.js";
import { pageHead, card, btn, field, input, select, toggle, dialog, confirm, toast, empty, spinner, errorState, windowsRow, accountState, kv, copyLine } from "../lib/components.js";

const base = "/v8/neo";
const accountPath = (id) => `${base}/vault/accounts/${encodeURIComponent(id)}`;
export async function render(ctx) {
	const admin = ctx.session.role === "admin";
	const host = h("div");
	ctx.root.append(pageHead({ title: "Account pools", desc: admin ? "Connect NeoPi sources or local provider accounts, assign owners and configure explicit local token budgets. Provider secrets stay with the credential owner." : "Your provider accounts, measured usage and pool-scoped inference keys. Ownership does not grant organization administration.", actions: [...(admin ? [btn("Connect NeoPi", { cls: "primary", onClick: () => sourceDialog(ctx, users) })] : []), btn("Add API key", { cls: "outline", onClick: () => addAPIKey(ctx) }), btn("Sign in provider", { icon: "plus", cls: "outline", onClick: () => login(ctx) })] }), host, spinner());
	let vault, user, users, sources;
	try {
		[vault, user, users, sources] = await Promise.all([neo.vault(), neo.user(ctx.session.userId), admin ? neo.users() : Promise.resolve([]), admin ? engine.get(`${base}/vault/sources`) : Promise.resolve([])]);
	} catch (e) { ctx.root.lastChild.replaceWith(errorState(e, () => ctx.rerender())); return; }
	ctx.root.lastChild.remove();
	if (admin) renderSources(host, ctx, sources, users);
	if (!vault.accounts.length) host.append(card({ body: empty({ icon: "shield", title: "No accounts", hint: admin ? "Sign in to a provider, or configure a provider credential in the engine. Assign an owner here before it is included in an owner's pool." : "You do not own any provider accounts yet. Sign in through this proxy, or ask an organization admin to assign an existing account." }) }));
	for (const account of vault.accounts) {
		const provider = vault.providers.find((p) => p.id === account.providerId);
		const body = h("div", null,
			kv([["Account", h("code", null, account.id)], ["Owner", admin ? (users.find((u) => u.id === account.ownerId)?.handle || account.ownerId || "unassigned") : ctx.session.handle], ["Plan", account.plan || "unknown"]]),
			windowsRow(account.windows),
			account.sourceId ? h("div", null, h("p", { class: "ink-2" }, "Shared through NeoPi. Each account is pinned independently; signing out there revokes inference here."),
				h("p", { class: "ink-3" }, account.windows.some((w) => w.kind === "day") ? "Local day budget configured. Grant the generated virtual model to users, then issue a key scoped to the intended accounts." : "Next: configure a local day token budget below. Generated models do not route until a budget and user grant exist."),
				admin ? btn("Grant models to users", { cls: "outline sm", onClick: () => ctx.navigate("users") }) : null) : null,
			account.providerWindows?.length ? h("div", null, h("h4", null, "Provider-reported quota (separate from local budgets)"), account.providerWindows.map((w) => h("p", { class: "ink-3" }, `${w.kind}: ${w.known ? `${Math.round(w.used * 100)}% used${w.resetsAt ? ` · resets ${new Date(w.resetsAt).toLocaleString()}` : ""}` : "unknown"}`))) : account.sourceId ? h("p", { class: "ink-3" }, "Provider quota unknown. Missing native windows do not mean unlimited usage or no five-hour limit.") : null,
			h("p", { class: "ink-3" }, "The token capacities above are local routing budgets, not provider quota or monetary cost."),
		);
		if (admin) body.append(field("Assign owner", select([{ value: "", label: "Choose an owner" }, ...users.filter((u) => ["admin", "owner"].includes(u.role)).map((u) => ({ value: u.id, label: `${u.handle} · ${u.role}` }))], account.ownerId ?? "", async (ownerId) => {
			if (!ownerId) return;
			try { await engine.patch(accountPath(account.id), { ownerId }); toast("Account owner saved"); ctx.rerender(); }
			catch (e) { toast(e.message, "bad"); ctx.rerender(); }
		})));
		host.append(card({ title: h("span", { class: "row" }, provider?.name ?? account.providerId, h("code", null, account.label), accountState(account)), body, actions: [btn(account.sourceId ? "Configure local budget" : "Configure window", { cls: "outline sm", onClick: () => configureWindow(account, ctx) })] }));
	}

	const statsHost = h("div", null, spinner());
	host.append(card({ title: admin ? "Account usage ledger" : "Owned account statistics", sub: "Proxy-attributed token measurements. No client-local stats or guessed provider quota.", body: statsHost, actions: [btn("Refresh", { cls: "outline sm", onClick: loadStats })] }));
	async function loadStats() {
		try {
			const result = await engine.get(`${base}/owner/stats`);
			statsHost.replaceChildren();
			if (!result.accounts.length) statsHost.append(empty({ title: "No measured activity", hint: "Actual account attempts and token usage will appear here." }));
			for (const row of result.accounts) statsHost.append(card({ title: vault.accounts.find((a) => a.id === row.accountId)?.label ?? row.accountId, rise: false, body: kv([["Input tokens", fmtCompact(row.inputTokens)], ["Output tokens", fmtCompact(row.outputTokens)], ["Cached tokens", fmtCompact(row.cachedTokens)], ["Estimated tokens", fmtCompact(row.estimatedTokens)], ["Reserved tokens", fmtCompact(row.reservedTokens)], ["Attempts", fmtCompact(row.attempts)], ["Attempts with unknown usage", fmtCompact(row.unknownAttempts)]]) }));
		} catch (e) { statsHost.replaceChildren(errorState(e, loadStats)); }
	}
	await loadStats();

	const keysHost = h("div");
	function renderKeys() {
		keysHost.replaceChildren();
		if (!user.keys.length) keysHost.append(empty({ icon: "key", title: "No keys", hint: "Issue a key restricted to selected accounts in your pool." }));
		for (const key of user.keys) keysHost.append(h("div", { class: "li" }, h("div", { class: "lt" }, h("div", { class: "main" }, h("code", null, key.prefix), " · ", key.label), h("div", { class: "sub" }, `Created ${fmtAgo(key.createdAt)} · scope: ${key.accountIds?.length ? key.accountIds.map((id) => vault.accounts.find((a) => a.id === id)?.label ?? id).join(", ") : "all currently authorized accounts"}`)), btn("Revoke", { cls: "danger sm", onClick: async () => {
			if (!await confirm({ title: "Revoke this key?", text: `${key.label} stops working immediately.`, confirmLabel: "Revoke", danger: true })) return;
			try { await neo.revokeKey(user.id, key.id); user.keys = user.keys.filter((k) => k.id !== key.id); renderKeys(); toast("Key revoked"); }
			catch (e) { toast(e.message, "bad"); }
		} })));
	}
	renderKeys();
	host.append(card({ title: "Your inference keys", sub: "Keys retain current grants and can only narrow account access. They never sign in to the dashboard.", body: keysHost, actions: [btn("Issue scoped key", { cls: "outline sm", onClick: () => issueScopedKey(user, vault.accounts.filter((a) => a.ownerId === user.id), renderKeys) })] }));
}

function configureWindow(account, ctx) {
	let kind = account.sourceId ? "day" : "week", capacity = "", startsAt = new Date().toISOString().slice(0, 16);
	const err = h("div", { class: "error", role: "alert" });
	dialog({ title: `Meter ${account.label}`, sub: "Explicit local token capacity. Input and output consume this budget; this does not assert any provider quota.", body: [field("Window", select(["5h", "day", "week", "month"].map((v) => ({ value: v, label: v })), kind, (v) => { kind = v; })), field("Capacity in tokens", input({ type: "number", min: 1, step: 1, required: true, onInput: (e) => { capacity = e.target.value; } })), field("Window start (UTC)", input({ type: "datetime-local", value: startsAt, onInput: (e) => { startsAt = e.target.value; } })), err], actions: (close) => [btn("Cancel", { onClick: close }), btn("Save capacity", { cls: "primary", onClick: async () => {
		try {
			const capacityTokens = Number(capacity), start = Date.parse(`${startsAt}Z`);
			if (!Number.isSafeInteger(capacityTokens) || capacityTokens <= 0 || !Number.isFinite(start)) throw new Error("Enter a positive whole-token capacity and a valid UTC start.");
			await engine.put(`${accountPath(account.id)}/windows/${encodeURIComponent(kind)}`, { capacityTokens, startsAt: start });
			close(); toast("Metered window saved"); ctx.rerender();
		} catch (e) { err.textContent = e.message; }
	} })] });
}

function issueScopedKey(user, accounts, after) {
	let label = "";
	const selected = new Set();
	const err = h("div", { class: "error", role: "alert" });
	dialog({ title: "Issue pool-scoped key", sub: "Select owned accounts. The key intersects this scope with your current grants; it cannot acquire another owner's pool.", body: [field("Label", input({ onInput: (e) => { label = e.target.value; } })), accounts.length ? h("div", { class: "row wrap" }, accounts.map((a) => toggle(false, (enabled) => enabled ? selected.add(a.id) : selected.delete(a.id), `${a.providerId} · ${a.label}`))) : h("p", { class: "ink-3" }, "You have no owned accounts to scope a key to."), err], actions: (close) => [btn("Cancel", { onClick: close }), btn("Issue", { cls: "primary", disabled: !accounts.length, onClick: async () => {
		try {
			if (!selected.size) throw new Error("Select at least one owned account.");
			const { key, secret } = await neo.createKey(user.id, label || "pool key", [...selected]);
			user.keys.push(key); close(); after();
			dialog({ title: "Copy the key now", sub: "Shown once; NeoProxy stores only a hash. Keep this inference credential private.", body: copyLine(secret, "inference key"), actions: (done) => [btn("Done", { cls: "primary", onClick: done })] });
		} catch (e) { err.textContent = e.message; }
	} })] });
}

function login(ctx) {
	let provider = "codex", closed = false, timer, generation = 0;
	const result = h("div");
	const stop = () => { generation++; clearTimeout(timer); };
	const providerSelect = select(["codex", "anthropic", "gemini", "antigravity", "qwen", "iflow", "kimi", "kimi-ai", "devin", "xai", "meta"].map((v) => ({ value: v, label: v })), provider, (v) => { provider = v; stop(); result.replaceChildren(); });
	dialog({
		title: "Sign in provider",
		sub: "Initiated by your session. Only credentials created by this flow enter your owned pool.",
		body: [field("Provider", providerSelect), result],
		onClose: () => { closed = true; stop(); },
		actions: (close) => [btn("Close", { onClick: close }), btn("Start sign-in", {
			cls: "primary",
			onClick: async (e) => {
				const button = e.currentTarget;
				button.disabled = true;
				providerSelect.disabled = true;
				stop();
				const current = generation, selectedProvider = provider;
				try {
					const response = await engine.post(`${base}/vault/login/${encodeURIComponent(selectedProvider)}`, {});
					if (closed || current !== generation) return;
					if (!response.url || !response.state) throw new Error("Provider did not return a sign-in URL and state.");
					let code = "";
					const callbackError = h("div", { class: "error", role: "alert" });
					const callback = h("details", null, h("summary", null, "Provider asks you to paste an authorization code?"),
						field("Authorization code", input({ autocomplete: "off", onInput: (event) => { code = event.target.value; } })),
						btn("Submit code", { cls: "outline sm", onClick: async () => {
							try {
								await engine.post(`${base}/vault/login/callback`, { provider: selectedProvider, state: response.state, code: code.trim() });
								callbackError.textContent = "Code submitted. Waiting for provider confirmation.";
							} catch (error) { callbackError.textContent = error.message; }
						} }), callbackError,
					);
					result.replaceChildren(copyLine(response.url, "sign-in URL"), h("a", { class: "btn outline", href: response.url, target: "_blank", rel: "noopener noreferrer" }, "Open provider sign-in"), response.user_code ? field("Device code", copyLine(response.user_code)) : null, h("p", { class: "ink-3" }, "Waiting for provider sign-in. Closing stops polling."), callback);
					async function check() {
						try {
							const status = await engine.get(`${base}/vault/login/status`, { query: { state: response.state } });
							if (closed || current !== generation) return;
							if (status.status === "ok") { close(); toast("Provider account connected"); ctx.rerender(); }
							else if (status.status === "error") { result.replaceChildren(h("p", { class: "error" }, status.error || "Provider sign-in failed")); }
							else timer = setTimeout(check, 2000);
						} catch (error) { if (!closed && current === generation) result.replaceChildren(errorState(error)); }
					}
					timer = setTimeout(check, 2000);
				} catch (error) { if (!closed && current === generation) result.replaceChildren(errorState(error)); }
				finally { button.disabled = false; providerSelect.disabled = false; }
			},
		})],
	});
}

function addAPIKey(ctx) {
	let providerId = "codex", apiKey = "";
	const err = h("div", { class: "error", role: "alert" });
	dialog({
		title: "Add provider API key",
		sub: "Stored by this proxy's credential manager and owned by your user. Existing credentials cannot be claimed by submitting them again. Provider endpoints are fixed by the engine.",
		body: [
			field("Provider", select(["claude", "codex", "gemini", "xai", "meta"].map((v) => ({ value: v, label: v })), providerId, (v) => { providerId = v; })),
			field("Provider API key", input({ type: "password", autocomplete: "off", onInput: (e) => { apiKey = e.target.value; } }), "Not a dashboard password or a NeoProxy inference key."), err,
		],
		actions: (close) => [btn("Cancel", { onClick: close }), btn("Add account", {
			cls: "primary",
			onClick: async (e) => {
				const button = e.currentTarget;
				button.disabled = true;
				try {
					if (!apiKey.trim()) throw new Error("Enter the provider API key.");
					await engine.post(`${base}/vault/api-keys`, { providerId, apiKey: apiKey.trim() });
					apiKey = ""; close(); toast("Provider account added"); ctx.rerender();
				} catch (error) { err.textContent = error.message; }
				finally { button.disabled = false; }
			},
		})],
	});
}

function renderSources(host, ctx, sources, users) {
	const body = h("div", null,
		h("p", { class: "ink-2" }, "1. Connect an authenticated NeoPi gateway. 2. Set each account's local day budget below. 3. Grant its generated virtual models in Users and issue a scoped inference key. No provider login or refresh-token copy is needed."),
		h("p", { class: "ink-3" }, "Accounts and model entitlements reconcile automatically every 15 seconds. Failed discovery stops that source's accounts; a stale or logged-out credential cannot rotate to another NeoPi account."),
	);
	if (!sources.length) body.append(empty({ title: "No NeoPi sources connected", hint: "Use Connect NeoPi with your gateway address and its bearer token. Only organization admins can manage these connections." }));
	for (const source of sources) {
		const mutate = async (method, suffix = "", json) => {
			try {
				const result = method === "del" ? await engine.del(`${base}/vault/sources/${encodeURIComponent(source.id)}`) : await engine[method](`${base}/vault/sources/${encodeURIComponent(source.id)}${suffix}`, json ?? {});
				toast(result?.status && result.status !== "connected" && result.status !== "disabled" ? `Source: ${result.status}` : "Source updated", result?.status && !["connected", "disabled"].includes(result.status) ? "bad" : undefined);
				ctx.rerender();
			} catch (e) { toast(e.message, "bad"); }
		};
		body.append(card({
			title: `${source.label} · ${source.status}`,
			sub: `${source.url} · ${source.accountCount} accounts · ${source.modelCount} models · ${source.checkedAt ? `checked ${fmtAgo(source.checkedAt)}` : "not checked yet"}`,
			rise: false,
			body: kv([["Default owner for new accounts", users.find((u) => u.id === source.ownerId)?.handle || source.ownerId], ["Connection", source.enabled ? "Enabled · gateway bearer stored privately, never returned" : "Disabled · inference stopped"], ["Model setup", "Stored 1:1 virtual models; configure account budget, then grant access"]]),
			actions: [
				btn("Edit connection", { cls: "outline sm", onClick: () => sourceDialog(ctx, users, source) }),
				btn("Refresh accounts", { cls: "outline sm", disabled: !source.enabled, onClick: () => mutate("post", "/sync") }),
				btn(source.enabled ? "Disable" : "Enable", { cls: "outline sm", onClick: () => mutate("put", "", { label: source.label, url: source.url, ownerId: source.ownerId, enabled: !source.enabled }) }),
				btn("Disconnect", { cls: "danger sm", onClick: async () => {
					if (await confirm({ title: `Disconnect ${source.label}?`, text: "Inference from this source stops. Account ownership, local meters, virtual models and usage history remain; no NeoPi credential is deleted.", confirmLabel: "Disconnect", danger: true })) await mutate("del");
				} }),
			],
		}));
	}
	host.append(card({ title: "NeoPi inference sources", body, actions: [btn("Connect NeoPi", { cls: "primary", onClick: () => sourceDialog(ctx, users) }), btn("Virtual models", { cls: "outline", onClick: () => ctx.navigate("virtual-models") }), btn("User grants & keys", { cls: "outline", onClick: () => ctx.navigate("users") })] }));
}

function sourceDialog(ctx, users = [], source) {
	let label = source?.label || "", url = source?.url || "", bearer = "", ownerId = source?.ownerId || ctx.session.userId;
	const err = h("div", { class: "error", role: "alert" });
	dialog({
		title: source ? "Edit NeoPi source" : "Connect NeoPi source",
		sub: "NeoPi keeps provider credentials and refresh authority. NeoProxy stores only this gateway connection in its private persistent state.",
		body: [
			field("Source name", input({ value: label, placeholder: "Office NeoPi", onInput: (e) => { label = e.target.value; } })),
			field("NeoPi gateway URL", input({ type: "url", value: url, placeholder: "https://gateway.example:PORT", onInput: (e) => { url = e.target.value; } }), "Use the authenticated NeoPi gateway, not this NeoProxy address. A trailing /v1 is optional. Prefer HTTPS; a bearer sent over HTTP needs a trusted private link."),
			field(source ? "Replace gateway bearer (optional)" : "Gateway bearer", input({ type: "password", autocomplete: "off", onInput: (e) => { bearer = e.target.value; } }), source ? "Leave blank to keep it. Changing the URL requires entering a bearer again." : "The gateway's bearer, not a provider API key, OAuth token or NeoProxy inference key."),
			field("Default account owner", select(users.filter((u) => ["admin", "owner"].includes(u.role) && u.state === "active").map((u) => ({ value: u.id, label: `${u.handle} · ${u.role}` })), ownerId, (v) => { ownerId = v; }), "Only newly discovered accounts use this default. Existing ownership assignments are preserved."),
			err,
		],
		actions: (close) => [btn("Cancel", { onClick: close }), btn(source ? "Save connection" : "Connect & discover accounts", { cls: "primary", onClick: async (event) => {
			const button = event.currentTarget; button.disabled = true;
			try {
				if (!label.trim() || !url.trim() || (!source && !bearer.trim())) throw new Error("Enter a source name, gateway URL and bearer.");
				const payload = { label: label.trim(), url: url.trim(), bearer: bearer.trim(), ownerId, enabled: source ? source.enabled : true };
				const result = source ? await engine.put(`${base}/vault/sources/${encodeURIComponent(source.id)}`, payload) : await engine.post(`${base}/vault/sources`, payload);
				bearer = ""; close();
				toast(result.status === "connected" ? "NeoPi connected. Configure local account budgets below, then grant generated models." : `Connection saved · ${result.status}`, result.status === "connected" || result.status === "disabled" ? undefined : "bad");
				ctx.rerender();
			} catch (e) { err.textContent = e.message; }
			finally { button.disabled = false; }
		} })],
	});
}
