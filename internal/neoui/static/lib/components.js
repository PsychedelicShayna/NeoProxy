// Shared widgets. Every page composes these; pages never write raw markup for
// a card, table, ring, chain or chip.

import { h, svgEl, icon, append, clear, fmtPct, fmtCompact, copyText } from "./dom.js";

// ---------- page chrome ----------

export function pageHead({ title, desc, actions = [], back }) {
	return h(
		"div",
		{ class: "page-head" },
		h(
			"div",
			{ class: "titles" },
			back ? h("a", { class: "back", href: back.href }, icon("arrowLeft", 14), back.label) : null,
			h("h1", null, title),
			desc ? h("p", { class: "desc" }, desc) : null,
		),
		actions.length ? h("div", { class: "actions" }, actions) : null,
	);
}

export function card({ title, sub, actions, body, foot, cls = "", flush = false, rise = true }) {
	return h(
		"section",
		{ class: `card ${rise ? "rise" : ""} ${cls}` },
		title || actions
			? h(
					"div",
					{ class: "card-head" },
					h("div", { class: "titles" }, h("div", { class: "title" }, title), sub ? h("div", { class: "sub" }, sub) : null),
					actions ? h("div", { class: "actions" }, actions) : null,
				)
			: null,
		body ? h("div", { class: `card-body ${flush ? "flush" : ""}` }, body) : null,
		foot ? h("div", { class: "card-foot" }, foot) : null,
	);
}

export function strip(cells) {
	return h(
		"div",
		{ class: "strip" },
		cells.map((c) =>
			h(
				"div",
				{ class: "cell" },
				h("div", { class: "label" }, c.label),
				h("div", { class: `value ${c.mono ? "mono" : ""}` }, c.value),
				c.sub ? h("div", { class: "sub" }, c.sub) : null,
			),
		),
	);
}

export function btn(label, { icon: ic, cls = "", onClick, title, disabled, href, type = "button" } = {}) {
	const content = [ic ? icon(ic, 15) : null, label];
	if (href) return h("a", { class: `btn ${cls}`, href, title, "aria-label": title }, content);
	return h("button", { class: `btn ${cls}`, type, onClick, title, "aria-label": title, disabled }, content);
}

export function iconBtn(ic, title, onClick, cls = "") {
	return btn(null, { icon: ic, cls: `icon ${cls}`, title, onClick });
}

export function badge(text, kind = "") {
	return h("span", { class: `badge ${kind}` }, text);
}

export function dot(kind = "") {
	return h("span", { class: `dot ${kind}` });
}

export function status(kind, text, sub) {
	return h("span", { class: "status" }, dot(kind), text, sub ? h("span", { class: "sub" }, sub) : null);
}

export function tag(text) {
	return h("span", { class: "tag" }, text);
}

// `neoproxy/codex/gpt-6-astra` rendered with the prefix quiet and the model
// loud; virtual names get the accent.
export function servedName(name, { virtual = false } = {}) {
	const parts = name.replace(/^neoproxy\//, "").split("/");
	if (parts.length === 1) return h("span", { class: "served" }, h("span", { class: "pre" }, "neoproxy/"), h("span", { class: virtual ? "vm" : "" }, parts[0]));
	return h("span", { class: "served" }, h("span", { class: "pre" }, "neoproxy/"), h("span", { class: "prov" }, `${parts[0]}/`), parts.slice(1).join("/"));
}

export function avatar(user, size = "") {
	const initials = (user.name || user.handle || "?")
		.split(/\s+/)
		.map((s) => s[0])
		.slice(0, 2)
		.join("")
		.toUpperCase();
	return h("span", { class: `avatar ${user.role} ${size}`, title: user.name }, user.role === "service" ? icon("cpu", 13) : initials);
}

// ---------- controls ----------

export function input(attrs = {}) {
	return h("input", { class: `input ${attrs.mono ? "mono" : ""}`, ...attrs });
}

export function select(options, value, onChange, attrs = {}) {
	return h(
		"select",
		{ class: "select", onChange: (e) => onChange(e.target.value), ...attrs },
		options.map((o) => h("option", { value: o.value, selected: o.value === value }, o.label)),
	);
}

export function search(placeholder, onInput, attrs = {}) {
	return h("div", { class: "search" }, icon("search", 15), input({ type: "search", placeholder, onInput: (e) => onInput(e.target.value), ...attrs }));
}

let fieldSerial = 0;
export function field(label, control, hint, error) {
	const target = control?.matches?.("input,textarea,select") ? control : control?.querySelector?.("input,textarea,select");
	if (target && !target.id) target.id = `neo-field-${++fieldSerial}`;
	return h("div", { class: "field" }, h("label", { for: target?.id }, label), control, error ? h("div", { class: "error" }, error) : hint ? h("div", { class: "hint" }, hint) : null);
}

export function seg(options, value, onChange) {
	const el = h("div", { class: "seg", role: "group" });
	for (const o of options) {
		el.append(
			h(
				"button",
				{
					type: "button",
					"aria-pressed": String(o.value === value),
					onClick: () => {
						for (const b of el.children) b.setAttribute("aria-pressed", String(b === btnEl));
						onChange(o.value);
					},
					title: o.title,
				},
				o.label,
			),
		);
		const btnEl = el.lastChild;
	}
	return el;
}

export function toggle(checked, onChange, label, { disabled = false, showLabel = true } = {}) {
	const el = h(
		"button",
		{
			type: "button",
			class: "switch",
			role: "switch",
			"aria-checked": String(checked),
			"aria-label": label,
			disabled,
			onClick: async () => {
				const next = el.getAttribute("aria-checked") !== "true";
				el.setAttribute("aria-checked", String(next));
				try {
					await onChange(next);
				} catch (e) {
					el.setAttribute("aria-checked", String(!next));
					throw e;
				}
			},
		},
		h("span", { class: "track" }),
		label && showLabel ? h("span", null, label) : null,
	);
	return el;
}

// ---------- rings (.neo-ring) ----------

// A ring whose fill is `used` (0..1 of the account window) and whose dashed
// marker is `budget` (0..1). Size in px. The label shows the used percentage.
export function ring({ used = null, budget = null, size = 36, label = true, large = false, title }) {
	const r = size / 2 - 3;
	const c = 2 * Math.PI * r;
	if (!Number.isFinite(used)) return h("span", { class: `neo-ring ${large ? "lg" : ""}`, title: title ?? "Usage unknown", role: "img", "aria-label": "Usage unknown", style: { width: `${size}px`, height: `${size}px` } }, h("span", { class: "ring-label" }, "?"));
	const u = Math.max(0, Math.min(1, used));
	const kind = budget !== null && u >= budget ? "bad" : budget !== null && u >= budget * 0.8 ? "warn" : u >= 0.9 ? "bad" : u >= 0.7 ? "warn" : "";
	const el = h("span", { class: `neo-ring ${kind} ${large ? "lg" : ""}`, title, role: "img", "aria-label": `${fmtPct(u, budget !== null ? null : 0)} used${budget !== null ? ` of ${fmtPct(budget, null)} allowed` : ""}` });
	const sv = svgEl("svg", { width: size, height: size, viewBox: `0 0 ${size} ${size}` });
	sv.append(svgEl("circle", { class: "track", cx: size / 2, cy: size / 2, r, "stroke-width": 3, fill: "none" }));
	if (budget !== null) {
		sv.append(
			svgEl("circle", {
				class: "budget",
				cx: size / 2,
				cy: size / 2,
				r: r + 0,
				"stroke-width": 1,
				fill: "none",
				"stroke-dasharray": `${c * Math.min(1, budget)} ${c}`,
			}),
		);
	}
	sv.append(svgEl("circle", { class: "fill", cx: size / 2, cy: size / 2, r, "stroke-width": 3, fill: "none", "stroke-dasharray": `${c} ${c}`, "stroke-dashoffset": c * (1 - u) }));
	el.append(sv);
	if (label) el.append(h("span", { class: "ring-label" }, fmtPct(u)));
	return el;
}

export function ringRow({ used, budget, main, sub, size = 36 }) {
	return h("div", { class: "ring-row" }, ring({ used, budget, size }), h("div", { class: "ring-text" }, h("div", { class: "main" }, main), sub ? h("div", { class: "sub" }, sub) : null));
}

// ---------- table ----------

export function table(columns, rows, { onRow, selectedKey, keyOf = (r) => r.id, emptyState, sort } = {}) {
	if (!rows.length && emptyState) return emptyState;
	let sortKey = sort?.key ?? null;
	let sortDir = sort?.dir ?? "desc";
	const tbl = h("table", { class: "table" });
	const thead = h("thead");
	const tbody = h("tbody");
	tbl.append(thead, tbody);
	const renderHead = () => {
		clear(thead);
		thead.append(
			h(
				"tr",
				null,
				columns.map((c) =>
					h(
						"th",
						{
							class: `${c.num ? "num" : ""} ${c.sortBy ? "sortable" : ""}`,
							style: c.width ? { width: c.width } : null,
							scope: "col",
							"aria-sort": c.sortBy ? (sortKey === c.key ? (sortDir === "asc" ? "ascending" : "descending") : "none") : null,
						},
						c.sortBy ? h("button", {
							type: "button",
							class: "sort-button",
							dataset: { sort: c.key },
							onClick: (e) => {
								const focused = document.activeElement === e.currentTarget;
								if (sortKey === c.key) sortDir = sortDir === "asc" ? "desc" : "asc";
								else {
									sortKey = c.key;
									sortDir = "desc";
								}
								renderHead();
								renderBody();
								if (focused) thead.querySelector(`[data-sort="${CSS.escape(c.key)}"]`)?.focus({ preventScroll: true });
							},
						}, c.label, sortKey === c.key ? h("span", { "aria-hidden": "true" }, sortDir === "asc" ? " ↑" : " ↓") : null) : c.label,
					),
				),
			),
		);
	};
	const renderBody = () => {
		clear(tbody);
		let list = rows;
		if (sortKey) {
			const col = columns.find((c) => c.key === sortKey);
			list = rows.slice().sort((a, b) => {
				const va = col.sortBy(a);
				const vb = col.sortBy(b);
				const cmp = typeof va === "number" ? va - vb : String(va).localeCompare(String(vb));
				return sortDir === "asc" ? cmp : -cmp;
			});
		}
		for (const r of list) {
			const key = keyOf(r);
			tbody.append(
				h(
					"tr",
					{
						class: onRow ? "clickable" : "",
						"aria-selected": selectedKey !== undefined ? String(selectedKey === key) : null,
						onClick: onRow ? () => onRow(r) : null,
						tabindex: onRow ? 0 : null,
						onKeydown: onRow ? (e) => e.key === "Enter" && onRow(r) : null,
					},
					columns.map((c) => h("td", { class: `${c.num ? "num" : ""} ${c.wrap ? "wrap" : ""} ${c.clip ? "clip" : ""}` }, c.render(r))),
				),
			);
		}
	};
	renderHead();
	renderBody();
	return h("div", { class: "table-scroll", tabindex: 0, role: "region", "aria-label": "Table; scroll horizontally for more columns" }, tbl);
}

export function empty({ icon: ic = "inbox", title, hint, action }) {
	return h("div", { class: "empty" }, icon(ic, 22), h("div", { class: "title" }, title), hint ? h("div", { class: "hint" }, hint) : null, action);
}

export function kv(pairs) {
	return h(
		"dl",
		{ class: "kv" },
		pairs.filter(Boolean).map(([k, v]) => [h("dt", null, k), h("dd", null, v)]),
	);
}

export function copyLine(text, label) {
	const el = h("span", { class: "row" }, h("code", null, text));
	el.append(
		iconBtn("copy", `Copy ${label ?? ""}`.trim(), async () => {
			toast((await copyText(text)) ? "Copied" : "Clipboard unavailable", "warn");
		}, "sm"),
	);
	return el;
}

// ---------- inspector (docked right panel) ----------

export function inspector({ title, sub, body, foot, onClose }) {
	return h(
		"aside",
		{ class: "inspector", "aria-label": title },
		h(
			"div",
			{ class: "ins-head" },
			h("div", { class: "titles" }, h("h2", null, title), sub ? h("div", { class: "sub" }, sub) : null),
			onClose ? iconBtn("x", "Close", onClose, "sm") : null,
		),
		h("div", { class: "ins-body" }, body),
		foot ? h("div", { class: "ins-foot" }, foot) : null,
	);
}

export function section(title, ...children) {
	return h("div", { class: "section" }, title ? h("div", { class: "section-title" }, title) : null, children);
}

// ---------- feedback ----------

export function toast(text, kind = "ok", ms = 2800) {
	const host = document.getElementById("toasts");
	const el = h("div", { class: "toast", role: "status" }, dot(kind), text);
	host.append(el);
	setTimeout(() => el.remove(), ms);
	return el;
}

const openDialogs = new Set();
export function dismissDialogs() {
	for (const close of [...openDialogs]) close();
}

export function dialog({ title, sub, body, actions, onClose }) {
	const backdrop = h("div", { class: "dialog-backdrop", onClick: (e) => e.target === backdrop && close() });
	const close = () => {
		if (!openDialogs.delete(close)) return;
		backdrop.remove();
		document.removeEventListener("keydown", esc);
		onClose?.();
	};
	const esc = (e) => e.key === "Escape" && close();
	openDialogs.add(close);
	document.addEventListener("keydown", esc);
	backdrop.append(h("div", { class: "dialog", role: "dialog", "aria-modal": "true", "aria-label": title }, h("h2", null, title), sub ? h("div", { class: "sub" }, sub) : null, body, actions ? h("div", { class: "actions" }, actions(close)) : null));
	document.body.append(backdrop);
	backdrop.querySelector("input,button,textarea,select")?.focus();
	return { close };
}

export function confirm({ title, text, confirmLabel = "Confirm", danger = false }) {
	// Resolve before closing: close() fires onClose, which must only count as a
	// cancel when nothing has been decided yet.
	let settled = false;
	const { promise, resolve } = Promise.withResolvers();
	const decide = (v, close) => {
		if (!settled) {
			settled = true;
			resolve(v);
		}
		close();
	};
	dialog({
		title,
		sub: text,
		actions: (close) => [
			btn("Cancel", { onClick: () => decide(false, close) }),
			btn(confirmLabel, { cls: danger ? "danger outline" : "primary", onClick: () => decide(true, close) }),
		],
		onClose: () => {
			if (!settled) {
				settled = true;
				resolve(false);
			}
		},
	});
	return promise;
}

export function spinner(text = "Loading") {
	return h("div", { class: "empty" }, h("div", { class: "hint" }, text, "…"));
}

export function errorState(err, retry) {
	return empty({ icon: "info", title: "Could not load", hint: err?.message ?? String(err), action: retry ? btn("Try again", { icon: "refresh", cls: "outline", onClick: retry }) : null });
}

// ---------- provider → model picker ----------

// Never a flat list: a provider column (with account counts) and the models of
// the selected provider. `models` and `providers` are the catalogue; `accounts`
// gives per-provider counts.
export function modelPicker({ providers, models, accounts, value, onPick, onlyExposed = true }) {
	let providerId = value ? value.split("/")[0] : providers.find((p) => p.exposed)?.id;
	let query = "";
	const el = h("div", { class: "picker" });
	const left = h("div", { class: "p-col", role: "listbox", "aria-label": "Providers" });
	const right = h("div", { class: "p-col", role: "listbox", "aria-label": "Models" });
	el.append(left, right);
	const render = () => {
		clear(left);
		clear(right);
		for (const p of providers) {
			if (onlyExposed && !p.exposed) continue;
			const n = accounts.filter((a) => a.providerId === p.id && a.state === "healthy").length;
			left.append(
				h(
					"button",
					{
						type: "button",
						class: `p-item ${n === 0 ? "disabled" : ""}`,
						role: "option",
						"aria-selected": String(p.id === providerId),
						onClick: () => {
							providerId = p.id;
							render();
						},
					},
					p.name,
					h("span", { class: "spacer" }),
					h("span", { class: "count" }, `${n}`),
				),
			);
		}
		const filter = h("input", {
			class: "input",
			type: "search",
			placeholder: "Filter models",
			value: query,
			style: { marginBottom: "6px", height: "28px" },
			onInput: (e) => {
				query = e.target.value;
				renderModels();
			},
		});
		right.append(filter);
		const list = h("div");
		right.append(list);
		const renderModels = () => {
			clear(list);
			const q = query.toLowerCase();
			for (const m of models) {
				if (m.providerId !== providerId) continue;
				if (onlyExposed && !m.exposed) continue;
				if (q && !m.display.toLowerCase().includes(q) && !m.name.includes(q)) continue;
				list.append(
					h(
						"button",
						{
							type: "button",
							class: "p-item",
							role: "option",
							"aria-selected": String(m.id === value),
							onClick: () => {
								value = m.id;
								onPick(m);
								render();
							},
						},
						h("span", { class: "name", title: m.id }, m.display),
					),
				);
			}
			if (!list.children.length) list.append(h("div", { class: "empty", style: { padding: "20px" } }, h("div", { class: "hint" }, "No models match")));
		};
		renderModels();
	};
	render();
	return el;
}

// ---------- chain rail (.neo-chain) ----------

// Renders a virtual model's stages as a horizontal rail: request → stage… →
// terminal. `lit` marks the stage a simulated request would land on;
// `onSelect` makes nodes selectable; `onReorder` enables drag and Alt+Arrow keys.
export function chainRail(vm, { models, accounts, usageFor, lit = null, selected = null, onSelect, onAdd, onReorder, compact = false, editor = false, allot = 1, accountLabel = (a) => a.label } = {}) {
	const rail = h("div", { class: `neo-chain ${compact ? "compact" : ""} ${editor ? "editor" : ""}`, role: "list" });
	const link = (text) => h("div", { class: "link" }, icon("arrowRight", 14), text ? h("span", { class: "demote" }, text) : null);
	const litIdx = lit ? (lit.stageIndex ?? vm.stages.length) : null;
	if (!compact) rail.append(
		h(
			"div",
			{ class: "node request", role: "listitem" },
			h("div", { class: "n-head" }, "request"),
			h("div", { class: "n-model" }, servedName(vm.served ?? `neoproxy/${vm.name}`, { virtual: true })),
			compact ? null : h("div", { class: "n-foot" }, `${vm.stages.length} ${vm.stages.length === 1 ? "stage" : "stages"}`),
		),
	);
	vm.stages.forEach((s, i) => {
		if (!(compact && i === 0)) rail.append(link(i === 0 ? null : "demote"));
		const model = models.find((m) => m.id === s.model);
		const pool = accounts.filter((a) => a.providerId === (model?.providerId ?? s.model.split("/")[0]) && (s.accounts === "all" || s.accounts.includes(a.id)));
		const used = usageFor ? pool.reduce((n, a) => n + usageFor(s, a), 0) : 0;
		const cap = s.cap * allot;
		const isLit = litIdx === i;
		const node = h(
			"div",
			{
				class: `node ${isLit ? "lit" : ""}`,
				role: "listitem",
				"aria-selected": onSelect ? String(selected === s.id) : null,
				tabindex: onSelect ? 0 : null,
				onClick: onSelect ? () => onSelect(s) : null,
				onKeydown: (e) => {
					if (onReorder && e.altKey && !e.ctrlKey && !e.metaKey && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
						e.preventDefault();
						const j = e.key === "ArrowLeft" ? i - 1 : i + 1;
						if (j < 0 || j >= vm.stages.length) return onReorder(null, { id: s.id, edge: j < 0 ? "first" : "last" });
						const next = vm.stages.slice();
						[next[i], next[j]] = [next[j], next[i]];
						onReorder(next, { id: s.id, to: j });
					} else if (onSelect && (e.key === "Enter" || e.key === " ")) {
						e.preventDefault();
						onSelect(s);
					}
				},
				"aria-keyshortcuts": onReorder ? "Alt+ArrowLeft Alt+ArrowRight" : null,
				"aria-label": onSelect ? `Stage ${i + 1} of ${vm.stages.length}, ${model?.display ?? s.model}` : null,
				draggable: onReorder ? "true" : null,
				dataset: { stage: s.id },
			},
			h(
				"div",
				{ class: "n-head" },
				h("span", { class: "idx" }, `stage ${i + 1}`),
				h("span", { class: "spacer" }),
				s.per === "all" ? h("span", { title: "One budget shared by everyone granted" }, "shared") : null,
				s.effort ? h("span", { class: "badge violet" }, s.effort) : null,
			),
			h("div", { class: "n-model" }, model?.display ?? s.model, h("span", { class: "prov" }, servedName(`neoproxy/${s.model}`))),
			compact
				? null
				: h(
						"div",
						{ class: "n-accounts" },
						pool.map((a) =>
							ring({
								used: usageFor ? usageFor(s, a) : 0,
								budget: s.perAccount * allot,
								size: 26,
								label: false,
								title: `${accountLabel(a)}: ${fmtPct(usageFor ? usageFor(s, a) : 0, null)} of ${fmtPct(s.perAccount * allot, null)} allowed`,
							}),
						),
						h("span", { class: "ink-3", style: { fontSize: "11.5px" } }, drawLabel(s.draw, pool.length)),
					),
			compact
				? null
				: h(
						"div",
						{ class: "n-foot" },
						ring({ used: cap ? used / cap : 0, size: 22, label: false, title: `${fmtPct(used, null)} of the ${fmtPct(cap, null)} cap` }),
						h("span", null, `${fmtPct(used, null)} of ${fmtPct(cap, null)} · ${s.window}`),
					),
			onReorder ? h("span", { class: "grip", title: "Drag to reorder" }, icon("grip", 14)) : null,
		);
		if (onReorder) wireDrag(node, rail, vm, onReorder);
		rail.append(node);
	});
	if (onAdd) {
		rail.append(h("div", { class: "link adder" }, h("button", { type: "button", class: "add", onClick: onAdd, title: "Add a stage before the end", "aria-label": "Add stage" }, icon("plus", 14)), h("span", { class: "demote" }, "add stage")));
	} else rail.append(link(vm.stages.length ? "then" : null));
	const term = vm.terminal ?? { kind: "limit" };
	rail.append(
		h(
			"div",
			{
				class: `node terminal ${litIdx === vm.stages.length ? "lit" : ""}`,
				role: "listitem",
				"aria-selected": onSelect ? String(selected === "terminal") : null,
				tabindex: onSelect ? 0 : null,
				onClick: onSelect ? () => onSelect("terminal") : null,
				dataset: { stage: "terminal" },
				onKeydown: onSelect ? (e) => {
					if (e.key === "Enter" || e.key === " ") {
						e.preventDefault();
						onSelect("terminal");
					}
				} : null,
			},
			h("div", { class: "n-head" }, compact ? "then" : "when every stage is spent"),
			h("div", { class: "n-model" }, compact ? (term.kind === "queue" ? "queue" : term.kind === "passthrough" ? "fall through" : "limit error") : term.kind === "queue" ? "Queue until a window resets" : term.kind === "passthrough" ? "Fall through to a direct model" : "Return a limit error"),
			compact ? null : h("div", { class: "n-foot ink-3" }, term.kind === "queue" ? "The caller waits" : term.kind === "passthrough" ? servedName(`neoproxy/${term.model ?? ""}`) : "HTTP 429 with the message below"),
		),
	);
	return rail;
}

export function drawLabel(draw, n) {
	if (draw === "alternate") return n > 1 ? `alternating across ${n}` : "one account";
	if (draw === "least-used") return n > 1 ? `least-used of ${n}` : "one account";
	return n > 1 ? `fill ${n} in order` : "one account";
}

function wireDrag(node, rail, vm, onReorder) {
	node.addEventListener("dragstart", (e) => {
		e.dataTransfer.setData("text/plain", node.dataset.stage);
		e.dataTransfer.effectAllowed = "move";
		node.classList.add("dragging");
	});
	node.addEventListener("dragend", () => {
		node.classList.remove("dragging");
		for (const n of rail.querySelectorAll(".drop-before")) n.classList.remove("drop-before");
	});
	node.addEventListener("dragover", (e) => {
		e.preventDefault();
		node.classList.add("drop-before");
	});
	node.addEventListener("dragleave", () => node.classList.remove("drop-before"));
	node.addEventListener("drop", (e) => {
		e.preventDefault();
		const from = e.dataTransfer.getData("text/plain");
		const to = node.dataset.stage;
		if (!from || from === to) return;
		const ids = vm.stages.map((s) => s.id);
		const fi = ids.indexOf(from);
		const ti = ids.indexOf(to);
		if (fi === -1 || ti === -1) return;
		const next = vm.stages.slice();
		const [moved] = next.splice(fi, 1);
		next.splice(ti, 0, moved);
		onReorder(next, { id: moved.id, to: ti });
	});
}

// ---------- trail ----------

export function trail(decision, { models, accounts, users, admin = false }) {
	const steps = [];
	const modelName = (id) => models.find((m) => m.id === id)?.display ?? id;
	const accName = (id) => accounts.find((a) => a.id === id)?.label ?? "an account in the pool";
	(decision.trail ?? []).forEach((t, i) => {
		if (t.kind === "stage") {
			const cls = t.chosen && !t.skipped && decision.outcome === "ok" ? "served" : "skipped";
			let why;
			if (t.skipped === "upstream-error") why = `attempt${t.chosen ? ` from ${accName(t.chosen)}` : ""} failed${t.failureStatus ? ` (status ${t.failureStatus})` : ""}; the chain continued`;
			else if (t.skipped === "cap") why = `skipped: stage cap reached (${fmtPct(t.total, null)} of ${fmtPct(t.cap, null)})`;
			else if (t.skipped === "per-account") why = "skipped: every account at its per-account budget";
			else if (t.skipped === "unhealthy") why = "skipped: no eligible healthy, metered account in the pool";
			else if (t.skipped === "script") why = "skipped: the routing script selected another stage";
			else if (t.skipped === "lua-cap") why = `skipped: Lua stage cap reached (${fmtPct(t.total, null)} of ${fmtPct(t.cap, null)})`;
			else if (t.skipped === "lua-policy") why = "skipped: Lua conditions excluded the accounts or their calculated allocation is spent";
			else if (t.skipped) why = `skipped: ${t.skipped}`;
			else if (t.chosen) why = `selected ${accName(t.chosen)} · ${fmtPct(t.total, null)} of ${fmtPct(t.cap, null)} cap used${decision.outcome !== "ok" ? ` · request ${decision.outcome}` : ""}`;
			else why = "no account selected";
			const policy = admin && t.policy ? h("details", { style: { marginTop: "6px" } },
				h("summary", null, "Lua policy results"),
				t.policy.cap !== undefined ? h("div", { class: "why" }, `Calculated stage cap: ${fmtPct(t.policy.cap, null)} before grant scaling; effective ceiling ${fmtPct(t.cap, null)}.`) : null,
				t.policy.accounts.map((a) => {
					const candidate = t.candidates.find((c) => c.accountId === a.accountId);
					return h("div", { class: "why", style: { marginTop: "4px" } },
						`${accName(a.accountId)}: condition ${a.eligible ? "true" : "false"}${a.limit !== undefined ? `; calculated allocation ${fmtPct(a.limit, null)} before grant scaling` : ""}; ${candidate ? `effective budget ${fmtPct(candidate.budget, null)}, used ${fmtPct(candidate.used, null)}` : "not an eligible candidate"}.`);
				}),
			) : null;
			steps.push(h("div", { class: `step ${cls}` }, h("div", { class: "n" }, `${t.index + 1}`), h("div", null, h("div", { class: "what" }, modelName(t.model)), h("div", { class: "why" }, why), policy)));
		} else if (t.kind === "terminal") {
			const passthrough = t.terminal.kind === "passthrough";
			const title = passthrough ? "Fall through" : t.terminal.kind === "queue" ? "Queued" : "Limit error";
			const detail = passthrough ? (decision.outcome === "ok" ? `${modelName(decision.model)} · selected without a budget from ${accName(decision.accountId)}` : decision.message ?? decision.reason) : t.terminal.message || (t.terminal.kind === "queue" ? "Waiting for a stage that can serve." : "No stage could serve; returned a limit error.");
			steps.push(h("div", { class: `step ${passthrough && decision.outcome === "ok" ? "served" : "end"}` }, h("div", { class: "n" }, "end"), h("div", null, h("div", { class: "what" }, title), h("div", { class: "why" }, detail))));
		} else if (t.kind === "direct") {
			steps.push(h("div", { class: `step ${decision.outcome === "ok" ? "served" : "end"}` }, h("div", { class: "n" }, "→"), h("div", null, h("div", { class: "what" }, modelName(t.model)), h("div", { class: "why" }, `direct provider access · ${accName(t.accountId)}${decision.outcome !== "ok" ? ` · request ${decision.outcome}` : ""}`))));
		} else if (t.kind === "denied") {
			steps.push(h("div", { class: "step end" }, h("div", { class: "n" }, "✕"), h("div", null, h("div", { class: "what" }, "Denied"), h("div", { class: "why" }, t.virtualModel ? "This user is not granted that virtual model" : `This user has no direct access to ${t.providerId}`))));
		} else {
			steps.push(h("div", { class: "step end" }, h("div", { class: "n" }, "✕"), h("div", null, h("div", { class: "what" }, "Unknown model"), h("div", { class: "why" }, t.asked ?? ""))));
		}
	});
	return h("div", { class: "trail" }, steps);
}

export function outcomeBadge(outcome) {
	if (outcome === "ok") return badge("served", "ok");
	if (outcome === "limited") return badge("limited", "warn");
	if (outcome === "queued") return badge("queued", "cyan");
	return badge("error", "bad");
}

export function accountState(a) {
	if (a.state === "healthy") return status("ok", "Healthy");
	if (a.state === "cooldown") return status("warn", "Cooling down");
	if (a.state === "expired") return status("bad", "Token expired");
	return status("", "Disabled");
}

export function windowsRow(windows = [], { now = Date.now() } = {}) {
	if (!windows.length) return h("span", { class: "ink-3" }, "Quota unknown");
	return h(
		"div",
		{ class: "windows" },
		windows.map((w) => h("span", { class: "window" }, ring({ used: w.used, size: 24, label: false }), h("span", { class: "kind" }, w.kind),
			h("span", null, w.known && w.unit === "tokens" ? `${fmtCompact(w.usedTokens)} of ${fmtCompact(w.capacity)} tokens` : `${fmtPct(w.used)} used`),
			w.source === "configured-metered" ? tag("local meter") : null,
			h("span", { class: "ink-3" }, `· resets in ${fmtInWindow(w.resetsAt, now)}`))),
	);
}

function fmtInWindow(ts, now) {
	const m = Math.max(0, Math.round((ts - now) / 60000));
	if (m < 60) return `${m}m`;
	const hr = Math.floor(m / 60);
	if (hr < 24) return `${hr}h ${m % 60}m`;
	return `${Math.floor(hr / 24)}d ${hr % 24}h`;
}

export { append, clear };
