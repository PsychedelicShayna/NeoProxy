// Models: everything the proxy serves, as a tree by provider. Never a flat
// list. Virtual models sit on top because they are what users should reach
// for; provider models follow, grouped and collapsible, each with a switch.

import { h, icon, fmtCompact, plural, clear } from "../lib/dom.js";
import { neo, onChange } from "../lib/client.js";
import { pageHead, card, btn, badge, tag, toggle, search, seg, servedName, chainRail, empty, spinner, errorState, toast, avatar } from "../lib/components.js";

export async function render(ctx) {
	const { root } = ctx;
	root.append(
		pageHead({
			title: "Models",
			desc: "Everything this proxy answers to, by provider. A switch here changes what every user sees; a user's own access is set on their page.",
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
	const collapsed = new Set();
	const reqCount = (served) => requests.filter((r) => r.asked === served || r.decision?.model === served.replace(/^neoproxy\//, "")).length;
	const usersWith = (m) => users.filter((u) => u.providerAccess.includes(m.providerId)).length;

	const summary = h("span", { class: "ink-3 summary", style: { fontSize: "12.5px" } });
	const toolbar = h(
		"div",
		{ class: "row models-toolbar", style: { marginBottom: "14px" } },
		search("Filter by name, provider or tag", (v) => {
			query = v.toLowerCase();
			renderTree();
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

	// virtual models on top
	const vmCard = card({
		title: "Virtual models",
		sub: "Names you defined. Users reach for these first; the chain decides what actually answers.",
		actions: [btn("Manage", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate("virtual-models") })],
		flush: true,
		body: h(
			"div",
			{ class: "list" },
			vms.map((vm) =>
				h(
					"button",
					{ type: "button", class: "li clickable vm-teaser", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) },
					h("div", { class: "lt" }, h("div", { class: "main" }, servedName(vm.served, { virtual: true })), h("div", { class: "sub" }, `${plural(users.filter((u) => u.grants.some((g) => g.virtualModel === vm.id)).length, "user")} · ${reqCount(vm.served)} requests recently`)),
					h("div", { class: "rail-clip" }, chainRail(vm, { models, accounts: vault.accounts, compact: true })),
					icon("chevronRight", 14),
				),
			),
		),
	});
	host.append(vmCard);

	const treeCard = card({ title: "Provider models", sub: "Local providers may have direct selectors. NeoPi source models use the generated stored virtual names shown below. Hiding a provider disables all routes that draw from it.", flush: true, body: h("div", { class: "tree" }) });
	host.append(treeCard);
	const tree = treeCard.querySelector(".tree");

	function matches(m, p) {
		if (show === "served" && !(m.exposed && p.exposed)) return false;
		if (show === "hidden" && m.exposed && p.exposed) return false;
		if (!query) return true;
		return m.display.toLowerCase().includes(query) || m.id.includes(query) || p.name.toLowerCase().includes(query) || m.tags.some((t) => t.includes(query));
	}

	function renderTree() {
		summary.textContent = `${models.filter((m) => m.exposed && vault.providers.find((p) => p.id === m.providerId)?.exposed).length} of ${models.length} provider models served · ${vms.length} virtual`;
		clear(tree);
		let any = false;
		for (const p of vault.providers) {
			const all = models.filter((m) => m.providerId === p.id);
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
							"aria-label": `${isCollapsed ? "Expand" : "Collapse"} ${p.name}`,
						},
						h("span", { class: "caret" }, icon("chevronDown", 14)),
						p.name,
						h("span", { class: "ink-3", style: { fontWeight: 400 } }, p.kind === "neopi-gateway" ? "virtual models only" : `neoproxy/${p.id}/…`),
					),
					h("div", { class: "meta" }, h("span", null, `${all.filter((m) => m.exposed).length} of ${plural(all.length, "model")}`), h("span", null, plural(accs.length, "account")), h("span", null, p.kind === "neopi-gateway" ? "grant virtual models in Users" : `${plural(users.filter((u) => u.providerAccess.includes(p.id)).length, "user")} direct`)),
					toggle(p.exposed, async (v) => {
						await neo.setProviderExposed(p.id, v);
						p.exposed = v;
						toast(v ? `${p.name} served` : `${p.name} hidden from everyone`);
						renderTree();
					}, `Serve ${p.name}`),
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
	const off = onChange((w) => (w === "vault" || w === "models") && renderTree());
	return () => off();
}
