// Models: everything the proxy serves, as a tree by provider. Never a flat
// list. Virtual models sit on top because they are what users should reach
// for; provider models follow, grouped and collapsible, each with a switch.

import { h, icon, fmtCompact, plural, clear } from "../lib/dom.js";
import { neo, onChange } from "../lib/client.js";
import { pageHead, card, btn, tag, toggle, search, seg, servedName, chainRail, providerLabel, vmSearchText, vmIdentity, empty, spinner, errorState, toast } from "../lib/components.js";

export async function render(ctx) {
	const { root } = ctx;
	root.append(
		pageHead({
			title: "Models",
			desc: "What this proxy serves. Users ask for virtual models; a provider's models are reachable directly only where the provider allows it. A switch here changes what every user sees.",
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let models, vault, vms, users, requests;
	try {
		[models, vault, vms, users, requests] = await Promise.all([neo.models(), neo.vault(), neo.virtualModels(), neo.users(), neo.requests({ limit: 200 })]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	let query = "";
	let show = "all"; // all | served | hidden
	let vmLimit = 25;
	const vmSearch = new Map(vms.map((vm) => [vm.id, vmSearchText(vm, models, vault.providers)]));
	const collapsed = new Set();
	const reqCount = (served) => requests.filter((r) => r.asked === served || r.decision?.model === served.replace(/^neoproxy\//, "")).length;
	const providers = new Map(vault.providers.map((p) => [p.id, p]));

	const summary = h("span", { class: "ink-3 summary", role: "status", "aria-live": "polite", style: { fontSize: "12.5px" } });
	const toolbar = h(
		"div",
		{ class: "row models-toolbar", style: { marginBottom: "14px" } },
		search("Filter by name, provider or tag", (v) => {
			query = v.trim().toLowerCase();
			vmLimit = 25;
			renderTree();
			renderVMs();
		}),
		seg(
			[
				{ value: "all", label: "All" },
				{ value: "served", label: "Served" },
				{ value: "hidden", label: "Hidden" },
			],
			show,
			(v) => {
				show = v;
				renderTree();
			},
		),
		h("span", { class: "spacer" }),
		summary,
	);
	host.append(toolbar);

	// Virtual teasers share the provider search, not its exposure filter.
	const vmList = h("div", { class: "list" });
	const vmMore = btn("Show 25 more", { cls: "outline sm", onClick: () => {
		const firstNew = vmLimit;
		vmLimit += 25;
		renderVMs();
		vmList.children[firstNew]?.focus();
	} });
	const vmCard = card({
		title: "Virtual models",
		sub: "Names users ask for. Shared-source models appear here as generated 1:1 virtual models; yours appear alongside them.",
		actions: [btn("Manage", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate("virtual-models") })],
		flush: true,
		body: [vmList, h("div", { style: { padding: "12px 16px" } }, vmMore)],
	});
	host.append(vmCard);

	const treeCard = card({ title: "Provider models", sub: "All / Served / Hidden filters only these provider models. Local providers may have direct selectors; shared-source models use stored virtual names. Hiding a provider disables all routes that draw from it.", flush: true, body: h("div", { class: "tree" }) });
	host.append(treeCard);
	const tree = treeCard.querySelector(".tree");

	function renderVMs() {
		const matches = vms.filter((vm) => !query || vmSearch.get(vm.id).includes(query));
		clear(vmList);
		for (const vm of matches.slice(0, vmLimit)) {
			vmList.append(h("button", {
				type: "button", class: "li clickable vm-teaser", title: vm.id,
				onClick: () => ctx.navigate(`virtual-models/${vm.id}`),
			},
			h("div", { class: "lt" }, h("div", { class: "main" }, servedName(vm.served, { virtual: true })), h("div", { class: "sub" }, `${vmIdentity(vm, models, vault.providers)} · ${plural(users.filter((u) => u.grants.some((g) => g.virtualModel === vm.id)).length, "user")} · ${reqCount(vm.served)} requests recently`)),
			h("div", { class: "rail-clip" }, chainRail(vm, { models, providers: vault.providers, accounts: vault.accounts, compact: true })),
			icon("chevronRight", 14)));
		}
		if (!matches.length) vmList.append(empty({ title: "No virtual models match", hint: "Try a provider, an upstream model name or a virtual name." }));
		vmMore.classList.toggle("hidden", matches.length <= vmLimit);
		const local = models.filter((m) => !m.virtualOnly);
		const servedLocal = local.filter((m) => m.exposed && providers.get(m.providerId)?.exposed).length;
		summary.textContent = `${servedLocal} of ${local.length} local provider models served · ${models.length - local.length} shared-source models (virtual only) · ${vms.length} virtual models · showing ${Math.min(vmLimit, matches.length)} of ${matches.length} virtual matches`;
	}

	function matches(m, p) {
		if (show === "served" && !(m.exposed && p.exposed)) return false;
		if (show === "hidden" && m.exposed && p.exposed) return false;
		if (!query) return true;
		const label = providerLabel(p);
		return [m.display, m.name, m.id, label.main, label.source, ...m.tags].some((value) => value?.toLowerCase().includes(query));
	}

	function renderTree() {
		clear(tree);
		let any = false;
		for (const p of vault.providers) {
			const all = models.filter((m) => m.providerId === p.id);
			const label = providerLabel(p);
			const list = all.filter((m) => matches(m, p));
			if (!list.length) continue;
			any = true;
			const accs = vault.accounts.filter((a) => a.providerId === p.id);
			const isCollapsed = collapsed.has(p.id) && !query;
			tree.append(
				h(
					"div",
					{ class: `group-row ${p.exposed ? "" : "off"}`, "aria-expanded": String(!isCollapsed) },
					h(
						"button",
						{
							type: "button",
							class: "name",
							onClick: () => {
								if (collapsed.has(p.id)) collapsed.delete(p.id);
								else collapsed.add(p.id);
								renderTree();
							},
							"aria-label": `${isCollapsed ? "Expand" : "Collapse"} ${label.main}`,
						},
						h("span", { class: "caret" }, icon("chevronDown", 14)),
						h("span", { title: p.id }, label.main),
						h("span", { class: "ink-3", style: { fontWeight: 400 } }, [label.source, p.kind === "neopi-gateway" ? "virtual models only" : `neoproxy/${p.id}/…`].filter(Boolean).join(" · ")),
					),
					h("div", { class: "meta" }, h("span", null, `${all.filter((m) => m.exposed).length} of ${plural(all.length, "model")}`), h("span", null, plural(accs.length, "account")), h("span", null, p.kind === "neopi-gateway" ? "grant virtual models in Users" : `${plural(users.filter((u) => u.providerAccess.includes(p.id)).length, "user")} direct`)),
					toggle(p.exposed, async (v) => {
						await neo.setProviderExposed(p.id, v);
						p.exposed = v;
						toast(v ? `${label.main} served` : `${label.main} hidden from everyone`);
						renderTree();
						renderVMs();
					}, `Serve ${label.main}`),
				),
			);
			if (isCollapsed) continue;
			for (const m of list) {
				tree.append(
					h(
						"div",
						{ class: `model-row ${m.exposed && p.exposed ? "" : "off"}` },
						h("div", null, h("div", { class: "cell-main" }, m.display), h("div", { class: "cell-sub" }, servedName(m.served))),
						h("div", { class: "tags" }, m.tags.map(tag)),
						h("div", { class: "num ink-3" }, m.context ? `${fmtCompact(m.context)} ctx` : "–"),
						h("div", { class: "num ink-3" }, `${reqCount(m.served)} req`),
						toggle(m.exposed, async (v) => {
							await neo.setModelExposed(m.id, v);
							m.exposed = v;
							toast(v ? `${m.display} served` : `${m.display} hidden`);
							renderTree();
							renderVMs();
						}, `Serve ${m.display}`, { disabled: !p.exposed, showLabel: false }),
					),
				);
			}
		}
		if (!any) tree.append(!vault.providers.length && !query && show === "all"
			? empty({ icon: "shield", title: "No provider models yet", hint: "Add a provider credential and its models appear here.", action: btn("Add credential", { icon: "plus", cls: "outline", onClick: () => ctx.navigate("credentials") }) })
			: empty({ icon: "search", title: "No models match", hint: "Try a provider name, a model id, or a tag like fast or reasoning." }));
	}
	renderTree();
	renderVMs();
	const off = onChange((w) => {
		if (w === "vault" || w === "models") {
			renderTree();
			renderVMs();
		}
	});
	return () => off();
}
