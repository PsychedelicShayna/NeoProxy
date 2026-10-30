// Proxy engine status and its authoritative provider credential vault.
// Providers can be hidden from users here; accounts show their quota windows
// and which virtual models draw from them.

import { h, icon, fmtAgo, fmtPct, fmtIn, plural } from "../lib/dom.js";
import { neo, onChange } from "../lib/client.js";
import { pageHead, card, kv, badge, status, toggle, btn, ring, windowsRow, accountState, inspector, section, servedName, spinner, errorState, toast, empty } from "../lib/components.js";

export async function render(ctx) {
	const { root } = ctx;
	root.append(
		pageHead({
			title: "Core & vault",
			desc: "Local provider accounts stay in this engine; shared accounts stay with their NeoPi broker. Exposure and scoped virtual models control who can use them.",
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let core, vault, vms, usage, users;
	try {
		[core, vault, vms, usage, users] = await Promise.all([neo.core(), neo.vault(), neo.virtualModels(), neo.usage(), neo.users()]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	host.append(
		card({
			title: h("span", { class: "row" }, "Engine", core.linked ? badge("connected", "ok") : badge("not connected", "warn")),
			sub: core.linked ? `Heartbeat ${fmtAgo(core.heartbeatAt)} · NeoProxy ${core.version} on ${core.host}` : "The engine could not report a live connection.",
			actions: [btn("Refresh", { icon: "refresh", cls: "outline", onClick: () => ctx.rerender() })],
			body: h(
				"div",
				{ class: "grid c2", style: { paddingTop: "4px" } },
				kv([
					["Endpoint", h("code", null, core.endpoint)],
					["Identity", h("code", null, core.identity)],
					["Scopes", h("span", { class: "row wrap" }, core.scopes.map((s) => badge(s)))],
				]),
				h(
					"p",
					{ class: "ink-2", style: { margin: 0 } },
					"Local credentials refresh in NeoProxy. Connected NeoPi gateways keep their own provider secrets and refresh authority; this proxy pins each shared account independently.",
				),
			),
		}),
	);

	const selected = { id: ctx.params[0] ?? null };
	const vaultCard = card({ title: "Vault", sub: `${plural(vault.providers.length, "provider")}, ${plural(vault.accounts.length, "account")} available to this proxy. Shared models are stored virtual routes with explicit local budgets, never direct provider selectors.`, actions: [btn("Connect NeoPi / manage pools", { cls: "outline", onClick: () => ctx.navigate("accounts") })], flush: true, body: h("div") });
	host.append(vaultCard);
	const listHost = vaultCard.querySelector(".card-body");

	const drawers = (accountId) => {
		const out = [];
		for (const vm of vms) for (const s of vm.stages) {
			const pid = s.model.split("/")[0];
			if (pid !== vault.accounts.find((a) => a.id === accountId)?.providerId) continue;
			if (s.accounts !== "all" && !s.accounts.includes(accountId)) continue;
			let used = 0;
			const per = [];
			for (const u of users) {
				const v = usage[u.id]?.[vm.id]?.[s.id]?.[accountId] ?? 0;
				if (v > 0) per.push([u.handle, v]);
				used += v;
			}
			out.push({ vm, stage: s, used, per });
		}
		return out;
	};

	const renderList = () => {
		listHost.replaceChildren();
		const tree = h("div", { class: "list" });
		if (!vault.providers.length) { listHost.append(empty({ icon: "shield", title: "No providers configured", hint: "Add a provider credential in this proxy to populate its vault and catalogue." })); return; }
		for (const p of vault.providers) {
			const accs = vault.accounts.filter((a) => a.providerId === p.id);
			tree.append(
				h(
					"div",
					{ class: "li", style: { background: "var(--panel)" } },
					h("div", { class: "lt" }, h("div", { class: "main row" }, p.name, h("span", { class: "ink-3", style: { fontWeight: 400 } }, p.vendor), badge(p.kind === "neopi-gateway" ? "NeoPi source" : p.kind === "oauth" ? "oauth" : "api key")), h("div", { class: "sub" }, p.exposed ? `${p.kind === "neopi-gateway" ? "stored virtual models only" : `served as neoproxy/${p.id}/…`} · ${plural(accs.length, "account")}` : "hidden from every user")),
					toggle(p.exposed, async (v) => {
						await neo.setProviderExposed(p.id, v);
						toast(v ? `${p.name} is served again` : `${p.name} hidden from users`);
					}, `Serve ${p.name}`),
				),
			);
			for (const a of accs) {
				const d = drawers(a.id);
				const names = [...new Set(d.map((x) => x.vm.name))];
				tree.append(
					h(
						"button",
						{ type: "button", class: "li clickable", "aria-selected": String(selected.id === a.id), style: { paddingLeft: "32px" }, onClick: () => open(a) },
						h("div", { class: "lt" }, h("div", { class: "main row" }, h("code", null, a.label), h("span", { class: "ink-3", style: { fontWeight: 400 } }, a.plan)), h("div", { class: "sub", title: names.join(", ") }, names.length ? `drawn by ${plural(names.length, "virtual model")}` : "no virtual model draws from this account")),
						windowsRow(a.windows),
						h("div", { style: { width: "130px", textAlign: "right" } }, accountState(a)),
					),
				);
			}
		}
		listHost.append(tree);
	};

	const open = (a) => {
		selected.id = a.id;
		renderList();
		const p = vault.providers.find((x) => x.id === a.providerId);
		const d = drawers(a.id);
		ctx.setInspector(
			inspector({
				title: `${p.name} · ${a.label}`,
				sub: `${a.plan} · ${a.state}`,
				onClose: () => {
					selected.id = null;
					renderList();
					ctx.setInspector(null);
				},
				body: [
					section(
						"Local token budgets",
						a.windows.length ? a.windows.map((w) => h("div", { class: "ring-row", style: { marginBottom: "8px" } }, ring({ used: w.used, size: 40 }), h("div", { class: "ring-text" }, h("div", { class: "main" }, `${w.kind} local budget`), h("div", { class: "sub" }, `${fmtPct(w.used)} used · resets in ${fmtIn(w.resetsAt)} · configured-metered`)))) : h("div", { class: "ink-3" }, "No local budget configured. Set an explicit metered capacity in Account pools before using budgeted routes."),
						a.state === "cooldown" ? h("div", { class: "ink-2", style: { fontSize: "12.5px" } }, `Cooling down after a rate limit; back in ${fmtIn(a.cooldownUntil)}.`) : null,
						a.state === "expired" ? h("div", { class: "ink-2", style: { fontSize: "12.5px" } }, "The credential expired. Re-authenticate through this proxy's provider login flow.") : null,
					),
					...(a.sourceId ? [section("Provider-reported quota", a.providerWindows?.length ? a.providerWindows.map((w) => h("p", { class: "ink-3" }, `${w.kind}: ${w.known ? `${fmtPct(w.used)} used${w.resetsAt ? ` · resets in ${fmtIn(w.resetsAt)}` : ""}` : "unknown"}`)) : h("p", { class: "ink-3" }, "Unknown. Missing native windows do not establish unlimited usage or absence of a five-hour limit."))] : []),
					section(
						"Drawn by",
						d.length
							? d.map((x) =>
									h(
										"div",
										{ style: { marginBottom: "10px" } },
										h("div", { class: "row" }, servedName(x.vm.served, { virtual: true }), h("span", { class: "ink-3" }, `stage ${x.vm.stages.indexOf(x.stage) + 1}`), h("span", { class: "spacer" }), h("span", { class: "num ink-2" }, fmtPct(x.used, 1))),
										x.per.length ? h("div", { class: "ink-3", style: { fontSize: "12px", marginTop: "2px" } }, x.per.map(([handle, v]) => `${handle} ${fmtPct(v, 1)}`).join(" · ")) : h("div", { class: "ink-3", style: { fontSize: "12px" } }, "nothing drawn yet this window"),
									),
								)
							: h("div", { class: "ink-3" }, a.sourceId ? "No virtual model includes this shared account. It cannot be reached through a direct provider selector." : "No virtual model includes this account. Direct-access users can still reach it."),
					),
					section("Account", kv([["Provider", p.name], ["Kind", p.kind === "neopi-gateway" ? "NeoPi broker-owned credential (no provider secret imported)" : p.kind === "oauth" ? "Proxy-managed OAuth login" : "Proxy-managed API key"], ["Owner", users.find((u) => u.id === a.ownerId)?.handle || a.ownerId || "unassigned"], ["Id", h("code", null, a.id)]])),
				],
				foot: [btn("Open in Virtual models", { icon: "arrowRight", cls: "outline", onClick: () => ctx.navigate("virtual-models") })],
			}),
		);
	};

	renderList();
	if (selected.id) {
		const a = vault.accounts.find((x) => x.id === selected.id);
		if (a) open(a);
	}

	host.append(
		card({
			title: "Credential administration",
			sub: "Local credentials use the existing manager. Shared inference uses authenticated NeoPi gateway connections.",
			actions: [btn("Connect NeoPi", { cls: "sm outline", onClick: () => ctx.navigate("accounts") }), btn("Local credentials", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate("credentials") })],
			body: h("p", { class: "ink-2", style: { margin: "4px 0 0" } }, "Organization admins connect sources and assign owners. Owners configure their local budgets and pool-scoped inference keys in Account pools. Source bearer credentials are never available to members."),
		}),
	);

	const off = onChange((what) => what === "vault" && ctx.rerender());
	return () => off();
}
