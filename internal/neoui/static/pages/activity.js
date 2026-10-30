// Activity: every request, with how it was routed. Admins see everyone;
// members see their own. Selecting a row docks the routing trail on the right.

import { h, icon, fmtTime, fmtDate, fmtCompact, fmtDuration, clear } from "../lib/dom.js";
import { neo } from "../lib/client.js";
import { pageHead, card, btn, badge, avatar, servedName, search, select, seg, table, inspector, section, kv, trail, outcomeBadge, empty, spinner, errorState } from "../lib/components.js";

export async function render(ctx) {
	const { root, session } = ctx;
	const mine = session.role !== "admin";
	root.append(
		pageHead({
			title: "Activity",
			desc: mine ? "Your requests and how each one was answered." : "Every request through the proxy, who asked, what they asked for, and which stage and account answered.",
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let users, models, vault, vms;
	try {
		[users, models, vault, vms] = await Promise.all([mine ? neo.user(session.userId).then((u) => [u]) : neo.users(), neo.models(), neo.vault(), neo.virtualModels()]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const filters = { userId: mine ? session.userId : ctx.query.get("user") ?? "", outcome: ctx.query.get("outcome") ?? "", asked: "" };
	let selectedId = ctx.params[0] ?? null;
	let rows = [];

	const toolbar = h(
		"div",
		{ class: "row wrap page-toolbar", style: { marginBottom: "14px" } },
		search("Filter by model name", (v) => {
			filters.asked = v;
			load();
		}),
		mine
			? null
			: select([{ value: "", label: "Everyone" }, ...users.map((u) => ({ value: u.id, label: u.handle }))], filters.userId, (v) => {
					filters.userId = v;
					load();
				}, { style: { width: "160px" } }),
		seg(
			[
				{ value: "", label: "All" },
				{ value: "ok", label: "Served" },
				{ value: "limited", label: "Limited" },
				{ value: "queued", label: "Queued" },
				{ value: "error", label: "Errors" },
			],
			filters.outcome,
			(v) => {
				filters.outcome = v;
				load();
			},
		),
		h("span", { class: "spacer" }),
		btn("Reload", { icon: "refresh", cls: "sm", onClick: load }),
	);
	const tableHost = h("div");
	host.append(toolbar, card({ flush: true, body: tableHost }));

	async function load() {
		try {
			rows = await neo.requests({ limit: 200, userId: filters.userId || undefined, outcome: filters.outcome || undefined, asked: filters.asked || undefined });
			renderTable();
		} catch (e) { tableHost.replaceChildren(errorState(e, load)); }
	}
	function renderTable() {
		clear(tableHost);
		tableHost.append(
			table(
				[
					{ key: "t", label: "Time", render: (r) => h("span", { class: "mono ink-3", dataset: { request: r.id } }, fmtTime(r.at)), sortBy: (r) => r.at },
					...(mine ? [] : [{ key: "who", label: "User", render: (r) => { const u = users.find((x) => x.id === r.userId); return h("span", { class: "row" }, avatar(u ?? { handle: "?" }, "sm"), u?.handle ?? r.userId); }, sortBy: (r) => r.userId }]),
					{ key: "client", label: "Client", clip: true, width: "12%", render: (r) => h("span", { class: "ink-2", title: r.client }, r.client) },
					{ key: "asked", label: "Asked for", clip: true, width: "26%", render: (r) => servedName(r.asked, { virtual: r.asked.split("/").length === 2 }), sortBy: (r) => r.asked },
					{ key: "served", label: "Answered by", clip: true, width: "34%", render: (r) => answeredBy(r) },
					{ key: "tok", label: "Tokens", num: true, render: (r) => (r.outcome === "ok" ? fmtCompact(r.tokens.in + r.tokens.out) : "–"), sortBy: (r) => r.tokens.in + r.tokens.out },
					{ key: "lat", label: "Latency", num: true, render: (r) => fmtDuration(r.latencyMs), sortBy: (r) => r.latencyMs },
					{ key: "out", label: "", render: (r) => outcomeBadge(r.outcome) },
				],
				rows,
				{ onRow: open, selectedKey: selectedId, emptyState: empty({ icon: "activity", title: "No requests match", hint: "Change the filter, or wait for traffic." }) },
			),
		);
	}
	function answeredBy(r) {
		const d = r.decision;
		if (d.outcome !== "ok") return h("span", { class: "ink-3" }, d.outcome === "limited" ? "limit reached, nothing served" : d.outcome === "queued" ? "queued for the next window" : d.reason);
		const m = models.find((x) => x.id === d.model);
		const a = vault.accounts.find((x) => x.id === d.accountId);
		return h("span", null, m?.display ?? d.model, d.stageIndex !== undefined ? h("span", { class: "ink-3" }, ` · stage ${d.stageIndex + 1}`) : h("span", { class: "ink-3" }, d.virtualModel ? " · fall through" : " · direct"), a && !mine ? h("span", { class: "ink-3" }, ` · ${a.label}`) : null);
	}
	function open(r) {
		selectedId = r.id;
		renderTable();
		const u = users.find((x) => x.id === r.userId);
		const key = u?.keys.find((k) => k.id === r.keyId);
		const vm = r.decision.virtualModel ? vms.find((v) => v.id === r.decision.virtualModel) : null;
		ctx.setInspector(
			inspector({
				title: h("span", null, servedName(r.asked, { virtual: !!vm })),
				sub: `${fmtDate(r.at)} · ${r.id}`,
				onClose: () => {
					selectedId = null;
					renderTable();
					ctx.setInspector(null);
				},
				body: [
					section("Outcome", h("div", { class: "row" }, outcomeBadge(r.outcome), r.decision.effort ? badge(`effort ${r.decision.effort}`, "violet") : null, r.stream ? badge("stream") : null)),
					section("How it was routed", trail(r.decision, { models, accounts: mine ? [] : vault.accounts, users, admin: !mine })),
					section(
						"Request",
						kv([
							["User", u ? h("span", { class: "row" }, avatar(u, "sm"), u.handle) : r.userId],
							key ? ["Key", h("code", null, `${key.prefix} · ${key.label}`)] : null,
							["Client", r.client],
							["Tokens", r.outcome === "ok" ? `${fmtCompact(r.tokens.in)} in · ${fmtCompact(r.tokens.out)} out · ${fmtCompact(r.tokens.cached)} cached` : "not reported"],
							["Latency", fmtDuration(r.latencyMs)],
						]),
					),
				],
				foot: [
					vm && !mine ? btn("Open chain", { icon: "route", cls: "outline", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) }) : null,
					mine ? null : btn("Open user", { icon: "user", cls: "outline", onClick: () => ctx.navigate(`users/${r.userId}`) }),
				],
			}),
			{ restoreFocus: () => {
				const row = tableHost.querySelector(`[data-request="${CSS.escape(r.id)}"]`)?.closest("tr");
				(row ?? tableHost.querySelector(".table-scroll") ?? toolbar.querySelector("input"))?.focus({ preventScroll: true });
			} },
		);
	}

	await load();
	if (selectedId) {
		const r = rows.find((x) => x.id === selectedId) ?? (await neo.request(selectedId).catch(() => null));
		if (r) open(r);
	}
}
