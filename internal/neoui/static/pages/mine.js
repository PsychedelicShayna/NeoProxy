// My models: what a signed-in member sees. Their catalogue with how much of
// each allotment is left, how to point a client at the proxy, and their keys.
// No admin surfaces; no provider internals beyond what their chain reveals.

import { h, icon, fmtPct, fmtAgo, fmtIn, copyText } from "../lib/dom.js";
import { neo, engine } from "../lib/client.js";
import { pageHead, card, btn, badge, servedName, ring, kv, chainRail, trail, toast, empty, spinner, errorState, field, input, dialog } from "../lib/components.js";

export async function render(ctx) {
	const { root, session } = ctx;
	root.append(
		pageHead({
			title: "My models",
			desc: "What you can ask this proxy for, and how much of each allotment is left. Everything is served under the neoproxy/ name; which provider answers is decided for you.",
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let user, vms, models, vault, usage;
	try {
		[user, vms, models, vault, usage] = await Promise.all([neo.user(session.userId), neo.virtualModels(), neo.models(), neo.vault(), neo.usage()]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const base = `${location.protocol}//${location.host}`;
	host.append(
		card({
			title: "Point a client here",
			sub: "Use your inference key as the bearer token and one of the names below as the model. Dashboard session cookies are not inference keys.",
			body: kv([
				["OpenAI Chat", h("code", null, `${base}/v1/chat/completions`)],
				["OpenAI Responses", h("code", null, `${base}/v1/responses`)],
				["Anthropic Messages", h("code", null, `${base}/v1/messages`)],
				["Model", h("code", null, user.grants.length ? `neoproxy/${vms.find((v) => v.id === user.grants[0].virtualModel)?.name}` : "ask your admin for a grant")],
			]),
		}),
	);

	if (!user.grants.length) {
		host.append(card({ body: empty({ icon: "route", title: "Nothing granted yet", hint: "Your admin has not given you a model. Ask for neoproxy/default to start." }) }));
	}
	for (const g of user.grants) {
		const vm = vms.find((v) => v.id === g.virtualModel);
		if (!vm) continue;
		const usageFor = (s, a) => usage[user.id]?.[vm.id]?.[s.id]?.[a.id] ?? 0;
		let sim;
		try { sim = await neo.simulate({ asked: vm.served, userId: user.id }); }
		catch (e) {
			host.append(card({ title: servedName(vm.served, { virtual: true }), body: errorState(e, () => ctx.rerender()) }));
			continue;
		}
		let drawn = 0;
		for (const st of Object.values(usage[user.id]?.[vm.id] ?? {})) for (const v of Object.values(st)) drawn += v;
		const total = g.allotment * vm.stages.reduce((n, s) => n + s.cap, 0);
		host.append(
			card({
				title: h("span", { class: "row" }, servedName(vm.served, { virtual: true }), sim.outcome === "ok" ? badge("available", "ok") : sim.outcome === "queued" ? badge("would queue", "cyan") : sim.outcome === "limited" ? badge("limit reached", "warn") : badge("unavailable", "bad")),
				sub: vm.description,
				actions: [h("div", { class: "ring-row" }, ring({ used: total ? drawn / total : 0, size: 40, large: true }), h("div", { class: "ring-text" }, h("div", { class: "main" }, "of your allotment used"), h("div", { class: "sub" }, g.allotment < 1 ? `you have ${fmtPct(g.allotment, null)} of the standard allotment` : "standard allotment")))],
				body: [
					chainRail(vm, { models, providers: vault.providers, accounts: vault.accounts, usageFor, lit: sim, allot: g.allotment, accountLabel: (a) => `account ${vault.accounts.filter((x) => x.providerId === a.providerId).indexOf(a) + 1}` }),
					sim.outcome !== "ok" ? h("div", { class: "ink-2", style: { marginTop: "8px" } }, sim.message) : h("div", { class: "ink-3", style: { marginTop: "6px", fontSize: "12.5px" } }, sim.stageIndex !== undefined ? `Right now a request lands on stage ${sim.stageIndex + 1}, ${models.find((m) => m.id === sim.model)?.display}.` : `Right now a request falls through to ${models.find((m) => m.id === sim.model)?.display}.`),
				],
			}),
		);
	}

	if (user.providerAccess.length) {
		host.append(
			card({
				title: "Direct provider access",
				sub: "Your admin let you call these providers by name. These draw from the account's own window, with no allotment.",
				body: h("div", { class: "row wrap", style: { paddingTop: "4px" } }, user.providerAccess.map((pid) => { const p = vault.providers.find((x) => x.id === pid); return p ? badge(`neoproxy/${p.id}/…`) : null; })),
			}),
		);
	}

	host.append(
		card({
			title: "Your keys",
			sub: "Ask your admin to issue or revoke keys.",
			flush: true,
			body: h(
				"div",
				{ class: "list" },
				user.keys.length ? user.keys.map((k) => h("div", { class: "li" }, icon("key", 15), h("div", { class: "lt" }, h("div", { class: "main" }, h("code", null, k.prefix), " ", h("span", { class: "ink-2", style: { fontWeight: 400 } }, k.label)), h("div", { class: "sub" }, `issued ${fmtAgo(k.createdAt)} · ${k.lastUsedAt ? `last used ${fmtAgo(k.lastUsedAt)}` : "never used"}`)))) : h("div", { class: "ink-3", style: { padding: "12px 16px" } }, "No keys yet."),
			),
		}),
	);
	host.append(card({
		title: "Dashboard password",
		sub: "Your password is separate from provider credentials and inference keys.",
		actions: [btn("Change password", { cls: "outline", onClick: () => {
			let currentPassword = "", password = "";
			const err = h("div", { class: "error", role: "alert" });
			dialog({ title: "Change password", sub: "All existing dashboard sessions and inference keys will be revoked. Sign in again and issue replacement keys after saving.", body: [
				field("Current password", input({ type: "password", autocomplete: "current-password", onInput: (e) => { currentPassword = e.target.value; } })),
				field("New password", input({ type: "password", autocomplete: "new-password", minlength: 12, onInput: (e) => { password = e.target.value; } }), "12–72 UTF-8 bytes."), err,
			], actions: (close) => [btn("Cancel", { onClick: close }), btn("Save password", { cls: "primary", onClick: async () => {
				try { await engine.post(`/v8/neo/users/${encodeURIComponent(user.id)}/password`, { currentPassword, password }); close(); toast("Password changed. Sign in again."); ctx.rerender(); }
				catch (e) { err.textContent = e.message; }
			} })] });
		} })],
	}));
}
