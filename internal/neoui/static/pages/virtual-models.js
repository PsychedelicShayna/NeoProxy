// Virtual models: names the proxy serves that are not a provider's model.
// Each one is a chain of stages; a stage is a real model drawn from a pool of
// accounts in measured sips. The catalogue shows every chain at a glance; the
// editor is a rail you click into, with a docked inspector for the selected
// stage and a simulator that shows where a user's request would land.

import { h, icon, fmtPct, fmtAgo, plural, clear } from "../lib/dom.js";
import { neo, onChange, ApiError } from "../lib/client.js";
import { pageHead, card, btn, badge, avatar, servedName, chainRail, inspector, section, field, input, select, seg, toggle, modelPicker, trail, ring, kv, confirm, toast, empty, spinner, errorState, drawLabel, search, vmIdentity, vmSearchText } from "../lib/components.js";

export async function render(ctx) {
	if (ctx.params[0]) return editor(ctx, ctx.params[0]);
	return catalogue(ctx);
}

// ---------- catalogue ----------

async function catalogue(ctx) {
	const { root } = ctx;
	let query = "", limit = 25;
	const summary = h("span", { class: "ink-3", role: "status", "aria-live": "polite" });
	root.append(pageHead({
		title: "Virtual models",
		desc: "Names users ask for. Shared models have generated 1:1 routes; custom chains appear alongside them.",
		actions: [
			search("Search by provider, model or name", (value) => { query = value.trim().toLowerCase(); limit = 25; paint(); }),
			btn("New virtual model", { icon: "plus", cls: "primary", onClick: () => ctx.navigate("virtual-models/new") }),
		],
	}));
	const host = h("div");
	root.append(summary, host, spinner());
	let vms, models, vault, users, usage;
	try {
		[vms, models, vault, users, usage] = await Promise.all([neo.virtualModels(), neo.models(), neo.vault(), neo.users(), neo.usage()]);
	} catch (error) {
		root.lastChild.replaceWith(errorState(error, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();
	const stageOwner = new Map();
	for (const vm of vms) for (const stage of vm.stages) stageOwner.set(stage, vm.id);
	const usageFor = (stage, account) => {
		let used = 0;
		for (const userUsage of Object.values(usage)) used += userUsage?.[stageOwner.get(stage)]?.[stage.id]?.[account.id] ?? 0;
		return used;
	};
	function paint() {
		if (!vms) return;
		clear(host);
		const matches = vms.filter((vm) => !query || vmSearchText(vm, models, vault.providers).includes(query));
		summary.textContent = `Showing ${Math.min(limit, matches.length)} of ${matches.length} matching virtual models · ${vms.length} total`;
		if (!matches.length) {
			host.append(card({ body: empty({
				icon: "route",
				title: vms.length ? "No virtual models match" : "No virtual models yet",
				hint: vms.length ? "Search by the upstream model, provider, source or served name." : "Create a name like neoproxy/default and choose its stages.",
			}) }));
			return;
		}
		for (const vm of matches.slice(0, limit)) {
			const granted = users.filter((user) => user.grants.some((grant) => grant.virtualModel === vm.id));
			const author = users.find((user) => user.id === vm.updatedBy || user.handle === vm.updatedBy)?.handle ?? "an administrator";
			const node = card({
				title: h("span", { class: "row" }, servedName(vm.served, { virtual: true })),
				sub: vmIdentity(vm, models, vault.providers),
				actions: [
					h("span", { class: "row", style: { gap: "2px", marginRight: "6px" } }, granted.slice(0, 5).map((user) => avatar(user, "sm")), granted.length > 5 ? h("span", { class: "ink-3", style: { fontSize: "12px", marginLeft: "4px" } }, `+${granted.length - 5}`) : null),
					btn("Edit", { icon: "arrowRight", cls: "outline sm", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) }),
				],
				body: chainRail(vm, { models, providers: vault.providers, accounts: vault.accounts, usageFor }),
				foot: [
					h("span", null, `Granted to ${granted.length ? granted.map((user) => user.handle).join(", ") : "nobody yet"}`),
					h("span", { class: "spacer" }),
					h("span", { class: "ink-3", title: vm.updatedBy }, `Updated ${fmtAgo(vm.updatedAt)} by ${author}`),
				],
			});
			node.dataset.vm = vm.id;
			host.append(node);
		}
		if (matches.length > limit) host.append(btn("Show 25 more", {
			cls: "outline sm",
			onClick: () => {
				const next = matches[limit].id;
				limit += 25;
				paint();
				host.querySelector(`[data-vm="${CSS.escape(next)}"] .btn`)?.focus();
			},
		}));
	}
	paint();
	const off = onChange((what) => what === "virtual-models" && ctx.rerender());
	return () => off();
}

// ---------- editor ----------

const newStage = (model) => ({ id: `s${Math.random().toString(36).slice(2, 7)}`, model, accounts: "all", draw: "least-used", perAccount: 0.05, cap: 0.2, window: "week", per: "user", effort: null });

const luaPoolExample = `-- Gateway window kinds may be namespaced, e.g. "zai:tokens:5h".
local function five_hour_used(ctx)
  local worst = nil
  for _, w in ipairs(ctx.account.providerWindows or {}) do
    local kind = w.kind
    if w.known and (kind == "5h" or string.sub(kind, -3) == ":5h") then
      if worst == nil or w.used > worst then worst = w.used end
    end
  end
  return worst -- nil means there is no known five-hour report.
end

function reported_room(ctx)
  local used = five_hour_used(ctx)
  if used == nil then return false end
  return used < 0.8
end

function available_share(ctx)
  local used = five_hour_used(ctx)
  if used == nil then return 0 end
  return math.min(0.05, (1 - used) * 0.1)
end

function pool_share(ctx)
  return 0.15
end`;

async function editor(ctx, id) {
	const { root } = ctx;
	const isNew = id === "new";
	root.append(spinner());
	let vm, models, vault, users, usage;
	try {
		[models, vault, users, usage] = await Promise.all([neo.models(), neo.vault(), neo.users(), neo.usage()]);
		vm = isNew
			? { id: null, name: "", served: "neoproxy/", description: "", stages: [], terminal: { kind: "limit", message: "" } }
			: await neo.virtualModel(id);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.navigate("virtual-models")));
		return;
	}
	root.lastChild.remove();

	const draft = structuredClone(vm);
	let dirty = isNew;
	let selected = matchMedia("(max-width: 900px)").matches ? null : draft.stages[0]?.id ?? null;
	let inspectorOpen = selected !== null;
	let simUser = users.find((u) => u.role === "member" && u.state === "active")?.id ?? users[0]?.id;
	let simResult = null;
	const errors = {};

	const usageFor = (s, a) => {
		let n = 0;
		for (const u of Object.values(usage)) n += u?.[draft.id]?.[s.id]?.[a.id] ?? 0;
		return n;
	};

	// ----- head -----
	const saveBtn = btn("Save", { icon: "save", cls: "primary", onClick: save });
	const head = pageHead({
		back: { href: "#/virtual-models", label: "Virtual models" },
		title: h("span", { class: "row", id: "vm-title" }, isNew ? "New virtual model" : servedName(draft.served, { virtual: true })),
		desc: isNew ? "Name it, add stages and save. Then try it against a user's actual grants." : null,
		actions: [
			isNew ? null : btn("Delete", { icon: "trash", cls: "danger", onClick: remove }),
			btn("Discard", { cls: "outline", onClick: () => (dirty ? discard() : ctx.navigate("virtual-models")) }),
			saveBtn,
		],
	});
	root.append(head);
	let resetSimulation = () => {};
	const setDirty = (v = true) => {
		dirty = v;
		saveBtn.textContent = "";
		saveBtn.append(icon("save", 15), dirty ? "Save changes" : "Saved");
		saveBtn.disabled = !dirty;
		if (v) resetSimulation();
	};

	// ----- identity -----
	const nameInput = input({
		mono: true,
		value: draft.name,
		placeholder: "default, small, heavy, review…",
		style: { paddingLeft: "92px" },
		onInput: (e) => {
			draft.name = e.target.value.trim().toLowerCase();
			draft.served = `neoproxy/${draft.name}`;
			errors.name = /^[a-z][a-z0-9-]{0,31}$/.test(draft.name) ? null : "lowercase letters, digits and dashes";
			if (models.some((m) => m.id === draft.name)) errors.name = "collides with a served model";
			renderIdentityErrors();
			renderRail();
			setDirty();
		},
	});
	const nameWrap = h("div", { style: { position: "relative" } }, h("span", { class: "served", style: { position: "absolute", left: "10px", top: "7px", pointerEvents: "none" }, "aria-hidden": "true" }, h("span", { class: "pre" }, "neoproxy/")), nameInput);
	const nameField = field("Name", nameWrap, "What users type as the model id. Keep it a role, not a brand: default, small, heavy.");
	const renderIdentityErrors = () => {
		const hint = nameField.querySelector(".hint, .error");
		hint.className = errors.name ? "error" : "hint";
		hint.textContent = errors.name ?? "What users type as the model id. Keep it a role, not a brand: default, small, heavy.";
	};
	root.append(
		card({
			title: "Identity",
			body: h(
				"div",
				{ class: "grid c2", style: { paddingTop: "4px" } },
				nameField,
				field("Description", h("textarea", { class: "textarea", style: { minHeight: "60px" }, placeholder: "One sentence a user would understand.", onInput: (e) => { draft.description = e.target.value; setDirty(); } }, draft.description), "Shown to users next to the name."),
			),
		}),
	);
	let scriptInput, scriptField;
	const scriptHint = "Maximum 16 KiB. Save validates syntax. Try it checks named functions, return types and routing with the selected user's current grants. No files, network, modules or credentials. Errors fail closed; no silent fallback.";
	const isScriptError = (e) => e instanceof ApiError && (["neoproxy_script", "neoproxy_script_error"].includes(e.body?.error?.code) || /\b(?:script|Lua)\b/i.test(e.message));
	function scriptMessage(message) {
		let readable = String(message);
		draft.stages.forEach((stage, index) => {
			const name = models.find((model) => model.id === stage.model)?.display ?? "Unavailable model";
			readable = readable.replaceAll(`stage "${stage.id}"`, `Stage ${index + 1} (${name})`);
		});
		return readable;
	}
	function setScriptError(message) {
		if (!scriptInput) return;
		scriptInput.setAttribute("aria-invalid", String(Boolean(message)));
		const hint = scriptField.querySelector(".hint, .error");
		hint.className = message ? "error" : "hint";
		hint.textContent = message ? scriptMessage(message) : scriptHint;
		hint.title = message || "";
	}
	function editScript() {
		if (matchMedia("(max-width: 900px)").matches && inspectorOpen) {
			inspectorOpen = false;
			renderRail();
			ctx.setInspector(null);
		}
		scriptInput?.focus();
		scriptInput?.scrollIntoView({ block: "center", behavior: "smooth" });
	}
	if (ctx.session.role === "admin") {
		const reportedKinds = [...new Set(vault.accounts.flatMap((account) => (account.providerWindows ?? []).map((window) => window.kind)))].sort();
		scriptInput = h("textarea", {
			class: "textarea mono", rows: 9, spellcheck: "false", "aria-label": "Lua routing script",
			placeholder: "Leave empty for standard routing",
			onInput: (e) => { draft.script = e.target.value; setScriptError(null); setDirty(); },
		}, draft.script ?? "");
		scriptField = field("Lua source", scriptInput, scriptHint);
		const hint = scriptField.querySelector(".hint");
		hint.id = `${scriptInput.id}-hint`;
		scriptInput.setAttribute("aria-describedby", hint.id);
		root.append(card({
			title: "Lua pool policy · optional",
			sub: "Use built-in routing unless you need a condition or a calculated budget. Choose Lua functions in each stage below; define them once here. Existing selector scripts may still return {stageId='s', accountId='a'}.",
			body: [
				scriptField,
				h("details", { style: { marginTop: "12px", maxWidth: "76ch" } },
					h("summary", null, "Function contracts & a reported-window example"),
					h("p", { class: "hint" }, "eligible(ctx) returns true or false per account. perAccount(ctx) returns a window fraction, not tokens or percent points: 0.05 means 5%. cap(ctx) returns the whole stage's fraction and has no account argument. Each stage names its functions; these names are yours to choose."),
					h("p", { class: "hint" }, "ctx.request, ctx.stage, ctx.allotment and (for account functions) ctx.account, ctx.window, ctx.used, ctx.budget are read-only. account.windows / ctx.window are configured local budget meters. account.providerWindows are separately reported native quotas. Check known before using used. Their kind names may be namespaced (provider:metric:window), so match the window suffix; unknown reporting does not mean there is no five-hour limit."),
					h("p", { class: "hint" }, "Lua limits can only reduce the static ceilings below; the user's allotment scales both. Static health, model/key/account scopes and atomic token reservations still win. Standard draw order chooses among accounts whose condition is true."),
					h("pre", { class: "mono", style: { whiteSpace: "pre-wrap", overflowWrap: "anywhere" } }, luaPoolExample),
					h("p", { class: "hint" }, "This example accepts an account only when a known five-hour report (kind 5h or ending in :5h, such as zai:tokens:5h) is below 80% used. Allocation shrinks as the most-constrained report fills, up to 5% of the local window; the stage cap is 15%. Unknown reports are excluded on purpose. Add a later built-in stage if you want those accounts as a fallback."),
					reportedKinds.length ? h("p", { class: "hint" }, `Kinds your accounts report right now: ${reportedKinds.join(", ")}`) : null,
					btn("Use example on selected stage", { cls: "outline sm", onClick: () => {
						const stage = draft.stages.find((s) => s.id === selected);
						if (!stage) { toast("Select a stage first.", "warn"); return; }
						if (draft.script?.trim()) { toast("The source is not empty. Copy the example functions and name them in the stage; existing source was not replaced.", "warn"); return; }
						draft.script = luaPoolExample;
						stage.lua = { eligible: "reported_room", perAccount: "available_share", cap: "pool_share" };
						scriptInput.value = draft.script;
						setScriptError(null);
						setDirty();
						renderInspector();
						editScript();
					} }),
				),
			],
		}));
	}

	// ----- chain -----
	const railHost = h("div");
	const reorderStatus = h("div", { class: "sr-only", role: "status" });
	root.append(
		card({
			title: "Chain",
			sub: "Left to right: a request arrives, each stage is tried in order, and when every stage is spent the chain ends the way you choose. Click a stage to edit it. To reorder, drag it, or focus it and press Alt+← or Alt+→.",
			body: [railHost, reorderStatus],
		}),
	);
	function renderRail(focusStage) {
		const focused = railHost.contains(document.activeElement) ? document.activeElement : null;
		focusStage ??= focused?.closest("[data-stage]")?.dataset.stage;
		clear(railHost);
		const lit = simResult;
		railHost.append(
			chainRail(draft, {
				models,
				providers: vault.providers,
				accounts: vault.accounts,
				usageFor,
				lit,
				selected,
				editor: true,
				allot: 1,
				onSelect: (s) => {
					selected = s === "terminal" ? "terminal" : s.id;
					inspectorOpen = true;
					renderRail(selected);
					renderInspector();
				},
				onAdd: addStage,
				onReorder: (next, info) => {
					if (!next) {
						reorderStatus.textContent = `Already the ${info.edge} stage`;
						return;
					}
					draft.stages = next;
					setDirty();
					renderRail(info.id);
					if (selected === info.id) renderInspector();
					const stage = next[info.to];
					reorderStatus.textContent = `Moved ${models.find((m) => m.id === stage.model)?.display ?? stage.model} to stage ${info.to + 1} of ${next.length}`;
				},
			}),
		);
		if (focusStage) railHost.querySelector(`[data-stage="${CSS.escape(focusStage)}"]`)?.focus({ preventScroll: true });
		if (focusStage) railHost.querySelector(`[data-stage="${CSS.escape(focusStage)}"]`)?.scrollIntoView({ inline: "nearest", block: "nearest" });
		const t = document.getElementById("vm-title");
		if (t && !isNew) {
			clear(t);
			t.append(servedName(draft.served, { virtual: true }));
		}
	}
	function addStage() {
		const usedProviders = new Set(draft.stages.map((s) => s.model.split("/")[0]));
		const candidate = models.find((m) => m.exposed && vault.providers.find((p) => p.id === m.providerId)?.exposed && !usedProviders.has(m.providerId)) ?? models.find((m) => m.exposed);
		if (!candidate) { toast("No exposed provider models are available. Add an account and expose its catalogue first.", "warn"); return; }
		const s = newStage(candidate.id);
		draft.stages.push(s);
		selected = s.id;
		inspectorOpen = true;
		setDirty();
		renderRail(selected);
		renderInspector();
		runSim();
	}

	// ----- simulator -----
	const simHost = h("div");
	let simProtocol = "openai", simPayload = "";
	const simProtocolSel = select([
		{ value: "openai", label: "OpenAI Chat Completions" },
		{ value: "openai-response", label: "OpenAI Responses" },
		{ value: "claude", label: "Anthropic Messages" },
	], simProtocol, (value) => { simProtocol = value; runSim(); }, { "aria-label": "Simulation protocol" });
	const payloadInput = h("textarea", {
		class: "textarea mono", rows: 6, spellcheck: "false", "aria-label": "Simulation request JSON",
		placeholder: 'Optional client JSON, e.g. {"messages":[{"role":"user","content":"Example request"}]}',
		onInput: (e) => {
			simPayload = e.target.value;
			++simGeneration;
			simResult = null;
			simHost.replaceChildren(h("div", { class: "ink-3" }, "Request changed. Run simulation to evaluate this payload."));
			renderRail();
		},
	});
	const simUserSel = select(
		users.map((u) => ({ value: u.id, label: `${u.handle} · ${u.role}` })),
		simUser,
		(v) => {
			simUser = v;
			runSim();
		},
		{ "aria-label": "Simulate as user" },
	);
	root.append(
		card({
			title: "Try it",
			sub: "Uses the saved model, the selected user's actual grants and current ledger. Save changes first; simulation never grants access or records usage.",
			actions: [h("div", { style: { width: "220px" } }, simUserSel), btn("Run simulation", { icon: "refresh", cls: "outline sm", onClick: runSim })],
			body: [
				h("details", { style: { marginBottom: "14px" } },
					h("summary", null, "Protocol & request payload"),
					field("Protocol", simProtocolSel),
					field("Client request JSON · optional", payloadInput, "Read-only script input.request. Use messages for Chat/Anthropic or input for Responses. The saved model and actual grants remain authoritative. Nothing is sent upstream or recorded. Script request bodies are capped at 256 KiB; the combined request/usage/health snapshot has its own 256 KiB cap and can reject smaller bodies."),
				),
				simHost,
			],
		}),
	);
	let simGeneration = 0;
	resetSimulation = () => {
		++simGeneration;
		simResult = null;
		simHost.replaceChildren(h("div", { class: "ink-3" }, "Save changes before running a real simulation."));
		renderRail();
	};
	async function runSim() {
		const generation = ++simGeneration;
		simResult = null;
		clear(simHost);
		if (dirty || isNew || !simUser) {
			simHost.append(h("div", { class: "ink-3" }, !simUser ? "No users available." : "Save changes before running a real simulation."));
			renderRail();
			return;
		}
		simHost.append(spinner());
		try {
			let payload;
			if (simPayload.trim()) {
				try { payload = JSON.parse(simPayload); }
				catch (e) { throw new Error(`Invalid simulation request JSON: ${e.message}`); }
				if (!payload || Array.isArray(payload) || typeof payload !== "object") throw new Error("Simulation request JSON must be an object.");
			}
			const result = await neo.simulate({ asked: vm.served, userId: simUser, protocol: simProtocol, request: payload });
			if (generation !== simGeneration || !simHost.isConnected) return;
			simResult = result;
			simHost.replaceChildren(trail(result, { models, accounts: vault.accounts, users, admin: ctx.session.role === "admin" }));
		} catch (e) {
			if (generation !== simGeneration || !simHost.isConnected) return;
			const failure = isScriptError(e)
				? empty({ icon: "info", title: "Routing script result rejected", hint: scriptMessage(e.message), action: scriptInput ? btn("Edit script", { cls: "outline", onClick: editScript }) : null })
				: errorState(e, runSim);
			failure.title = e.message;
			simHost.replaceChildren(failure);
		}
		renderRail();
	}

	// ----- grants -----
	const grantHost = h("div", { class: "list" });
	function renderGrants() {
		clear(grantHost);
		if (isNew) {
			grantHost.append(h("div", { class: "ink-3", style: { padding: "12px 16px" } }, "Save first, then grant it to users from their page."));
			return;
		}
		const granted = users.filter((u) => u.grants.some((g) => g.virtualModel === draft.id));
		if (!granted.length) grantHost.append(h("div", { class: "ink-3", style: { padding: "12px 16px" } }, "Nobody yet. Grant it from a user's page."));
		for (const u of granted) {
			const g = u.grants.find((x) => x.virtualModel === draft.id);
			let drawn = 0;
			for (const st of Object.values(usage[u.id]?.[draft.id] ?? {})) for (const v of Object.values(st)) drawn += v;
			grantHost.append(h("button", { type: "button", class: "li clickable", onClick: () => ctx.navigate(`users/${u.id}`) }, avatar(u), h("div", { class: "lt" }, h("div", { class: "main" }, u.handle), h("div", { class: "sub" }, `allotment ${fmtPct(g.allotment, null)} of every budget in this chain`)), h("span", { class: "num ink-2" }, `${fmtPct(drawn, 1)} drawn`), icon("chevronRight", 14)));
		}
	}
	root.append(card({ title: "Granted to", sub: "Each user's allotment scales every budget in this chain. Half an allotment is half the sips and half the cap.", flush: true, body: grantHost }));
	renderGrants();

	// ----- inspector -----
	function renderInspector() {
		if (!inspectorOpen || !selected) return ctx.setInspector(null);
		const stageId = selected;
		const stage = draft.stages.find((item) => item.id === stageId);
		if (stageId !== "terminal" && !stage) return ctx.setInspector(null);
		ctx.setInspector(stageId === "terminal" ? terminalInspector() : stageInspector(stage), {
			restoreFocus: () => railHost.querySelector(`[data-stage="${CSS.escape(stageId)}"]`)?.focus({ preventScroll: true }),
		});
	}
	function stageInspector(s) {
		const idx = draft.stages.indexOf(s);
		const model = models.find((m) => m.id === s.model);
		const pool = () => vault.accounts.filter((a) => a.providerId === s.model.split("/")[0]);
		const chosen = () => pool().filter((a) => s.accounts === "all" || s.accounts.includes(a.id));
		const update = () => {
			setDirty();
			renderRail();
			runSim();
		};
		const accountsSection = h("div");
		const renderAccounts = () => {
			clear(accountsSection);
			const all = pool();
			accountsSection.append(
				seg(
					[
						{ value: "all", label: `All ${all.length}` },
						{ value: "pick", label: "Pick" },
					],
					s.accounts === "all" ? "all" : "pick",
					(v) => {
						s.accounts = v === "all" ? "all" : all.map((a) => a.id);
						update();
						renderAccounts();
					},
				),
			);
			for (const a of all) {
				const on = s.accounts === "all" || s.accounts.includes(a.id);
				const w = a.windows.find((x) => x.kind === s.window);
				accountsSection.append(
					h(
						"label",
						{ class: "row", style: { padding: "8px 0", borderBottom: "1px solid var(--line-1)", cursor: s.accounts === "all" ? "default" : "pointer" } },
						h("input", {
							type: "checkbox",
							checked: on,
							disabled: s.accounts === "all",
							onChange: (e) => {
								const set = new Set(s.accounts === "all" ? all.map((x) => x.id) : s.accounts);
								if (e.target.checked) set.add(a.id);
								else set.delete(a.id);
								s.accounts = [...set];
								update();
							},
						}),
						ring({ used: w?.known ? w.used : null, size: 24, label: false, title: w?.known ? `${fmtPct(w.used)} of the configured ${w.kind} window used by everyone` : `No known configured ${s.window} budget window; native quota reporting is separate` }),
						h("div", { class: "lt", style: { flex: 1, minWidth: 0 } }, h("div", { style: { fontWeight: 500 } }, h("code", null, a.label)), h("div", { class: "ink-3", style: { fontSize: "12px" } }, `${a.plan} · ${a.state}`)),
						h("span", { class: "num ink-3", style: { fontSize: "12px" } }, `${fmtPct(usageFor(s, a), null)} by this stage`),
					),
				);
			}
		};
		renderAccounts();
		const luaControl = (key, label, builtin, suggested, hint) => {
			const enabled = Boolean(s.lua && Object.hasOwn(s.lua, key));
			const functionInput = input({
				mono: true, value: s.lua?.[key] ?? "", placeholder: suggested,
				maxLength: 64, pattern: "[A-Za-z_][A-Za-z0-9_]{0,63}",
				onInput: (e) => {
					s.lua[key] = e.target.value.trim();
					const valid = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(s.lua[key]);
					e.target.setAttribute("aria-invalid", String(!valid));
					functionHint.textContent = valid ? hint : "Use a function name: letters, digits and underscores; do not include quotes or ().";
					functionHint.className = valid ? "hint" : "error";
					update();
				},
			});
			const functionHint = h("div", { class: "hint" }, hint);
			const functionField = h("div", { class: enabled ? "" : "hidden" }, field("Function name", functionInput), functionHint);
			functionHint.id = `${functionInput.id}-policy-hint`;
			functionInput.setAttribute("aria-describedby", functionHint.id);
			return field(label, h("div", null,
				select([{ value: "builtin", label: builtin }, { value: "lua", label: "Lua function" }], enabled ? "lua" : "builtin", (mode) => {
					if (mode === "lua") {
						s.lua ??= {};
						s.lua[key] = functionInput.value.trim() || suggested;
						functionInput.value = s.lua[key];
					} else {
						delete s.lua?.[key];
						if (s.lua && Object.keys(s.lua).length === 0) delete s.lua;
					}
					functionField.classList.toggle("hidden", mode !== "lua");
					update();
				}),
				functionField,
			));
		};
		const pct = (v) => Number((v * 100).toPrecision(15));
		return inspector({
			title: `Stage ${idx + 1} · ${model?.display ?? s.model}`,
			sub: `${drawLabel(s.draw, chosen().length)} · ${fmtPct(s.perAccount, null)} each, ${fmtPct(s.cap, null)} cap · ${s.window}`,
			onClose: () => {
				inspectorOpen = false;
				renderRail();
				ctx.setInspector(null);
			},
			body: [
				section(
					"Model",
					modelPicker({
						providers: vault.providers,
						models,
						accounts: vault.accounts,
						value: s.model,
						onPick: (m) => {
							const changedProvider = m.providerId !== s.model.split("/")[0];
							s.model = m.id;
							if (changedProvider) s.accounts = "all";
							update();
							renderInspector();
							document.querySelector(`.inspector .picker [data-model="${CSS.escape(m.id)}"]`)?.focus();
						},
					}),
					h("div", { class: "hint", style: { marginTop: "6px", fontSize: "12px", color: "var(--ink-3)" } }, "Only providers the core holds and you serve. Hidden providers and models do not appear."),
				),
				section("Accounts in the pool", accountsSection),
				section(
					"How to draw",
					seg(
						[
							{ value: "alternate", label: "Alternate", title: "Each request moves to the next account" },
							{ value: "least-used", label: "Least used", title: "Pick the account with the most room" },
							{ value: "fill", label: "Fill", title: "Use the first account until its sip is gone, then the next" },
						],
						s.draw,
						(v) => {
							s.draw = v;
							update();
						},
					),
				),
				section(
					"Hard budget ceilings",
					h(
						"div",
						{ class: "grid c2" },
						field("Per account", h("div", { class: "row" }, input({ type: "number", min: 0, max: 100, step: 0.5, value: pct(s.perAccount), style: { width: "80px" }, onInput: (e) => { s.perAccount = Number(e.target.value) / 100; update(); } }), h("span", { class: "ink-3" }, "%")), "of each account's window"),
						field("Cap", h("div", { class: "row" }, input({ type: "number", min: 0, max: 100, step: 0.5, value: pct(s.cap), style: { width: "80px" }, onInput: (e) => { s.cap = Number(e.target.value) / 100; update(); } }), h("span", { class: "ink-3" }, "%")), "across the whole stage"),
					),
					field(
						"Window",
						seg(
							["5h", "day", "week", "month"].map((w) => ({ value: w, label: w })),
							s.window,
							(v) => {
								s.window = v;
								update();
								renderAccounts();
							},
						),
						"Budgets are a share of this quota window and reset with it.",
					),
					field(
						"Counted",
						seg(
							[
								{ value: "user", label: "Per user", title: "Every granted user gets this budget" },
								{ value: "all", label: "Shared", title: "One budget for everyone granted" },
							],
							s.per,
							(v) => {
								s.per = v;
								update();
							},
						),
					),
				),
				section(
					"Pool policy",
					h("p", { class: "hint" }, "Built-in choices need no Lua source. A Lua condition filters accounts; numeric functions reduce the hard ceilings above. The draw order still applies."),
					luaControl("eligible", "Account condition", "Built-in · all eligible accounts", "reported_room", "Must return a boolean. false excludes this account; missing or unknown native windows remain unknown."),
					luaControl("perAccount", "Per-account allocation", `Built-in · ${pct(s.perAccount)}% ceiling`, "available_share", "Must return a finite fraction from 0 to 1. 0.05 = 5% of this stage's local window, before grant scaling."),
					luaControl("cap", "Whole-stage cap", `Built-in · ${pct(s.cap)}% ceiling`, "pool_share", "Must return a finite fraction from 0 to 1. Called once per stage with ctx.stage and ctx.request; no ctx.account."),
					btn("Edit Lua source", { cls: "outline sm", onClick: editScript }),
				),
				section(
					"Effort",
					select(
						[
							{ value: "", label: "Caller's choice" },
							{ value: "low", label: "Force low" },
							{ value: "medium", label: "Force medium" },
							{ value: "high", label: "Force high" },
							{ value: "max", label: "Force max" },
						],
						s.effort ?? "",
						(v) => {
							s.effort = v || null;
							update();
						},
					),
				),
			],
			foot: [
				btn("Remove stage", {
					icon: "trash",
					cls: "danger",
					onClick: () => {
						draft.stages = draft.stages.filter((x) => x !== s);
						selected = draft.stages[0]?.id ?? null;
						inspectorOpen = selected !== null;
						update();
						renderInspector();
					},
				}),
			],
		});
	}
	function terminalInspector() {
		const t = draft.terminal;
		const msgField = field("Message to the caller", h("textarea", { class: "textarea", style: { minHeight: "80px" }, placeholder: "Your heavy allotment for this week is used up.", onInput: (e) => { t.message = e.target.value; setDirty(); } }, t.message ?? ""), "Returned in the error body so the user knows why, and when to try again.");
		const passField = h("div", { class: t.kind === "passthrough" ? "" : "hidden" });
		const renderPass = () => {
			clear(passField);
			passField.append(
				field(
					"Fall through to",
					modelPicker({
						providers: vault.providers,
						models: models.filter((m) => !m.virtualOnly),
						accounts: vault.accounts,
						value: t.model ?? null,
						onPick: (m) => {
							t.model = m.id;
							setDirty();
							renderRail();
							runSim();
						},
					}),
					"Served without a budget or a separate direct-access grant. Source-backed models require a metered stage and are not offered here.",
				),
			);
		};
		renderPass();
		return inspector({
			title: "When every stage is spent",
			sub: "The chain's end",
			onClose: () => {
				inspectorOpen = false;
				renderRail();
				ctx.setInspector(null);
			},
			body: [
				section(
					"Then",
					seg(
						[
							{ value: "limit", label: "Limit error" },
							{ value: "queue", label: "Queue" },
							{ value: "passthrough", label: "Fall through" },
						],
						t.kind,
						(v) => {
							t.kind = v;
							passField.classList.toggle("hidden", v !== "passthrough");
							setDirty();
							renderRail();
							runSim();
						},
					),
					h("p", { class: "ink-2", style: { fontSize: "12.5px", margin: "8px 0 0" } }, "A limit error is honest and immediate: HTTP 429 with your message. Queueing holds the request until a window resets, which suits batch callers. Falling through keeps serving from a model with no budget at all."),
				),
				section("", msgField),
				passField,
			],
		});
	}

	// ----- save / discard / delete -----
	async function save() {
		if (errors.name || !draft.name) {
			errors.name ??= "a name is required";
			renderIdentityErrors();
			nameInput.focus();
			return;
		}
		for (const stage of draft.stages) {
			if (!stage.lua) continue;
			const invalid = Object.values(stage.lua).some((name) => !/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name));
			if (invalid || !draft.script?.trim()) {
				setScriptError(invalid ? "Each Lua policy needs a simple function name (without quotes or parentheses)." : "This stage uses Lua functions. Add their source above, or choose Built-in in the stage.");
				selected = stage.id;
				inspectorOpen = true;
				renderInspector();
				editScript();
				return;
			}
		}
		try {
			const saved = await neo.saveVirtualModel(draft);
			setScriptError(null);
			toast(`Saved neoproxy/${saved.name}`);
			if (isNew) ctx.navigate(`virtual-models/${saved.id}`);
			else {
				Object.assign(vm, saved);
				Object.assign(draft, saved);
				setDirty(false);
				runSim();
			}
		} catch (e) {
			if (scriptInput && isScriptError(e)) {
				setScriptError(e.message);
				editScript();
				toast("Not saved: the routing script has an error.", "bad");
			} else toast(e instanceof ApiError ? e.message : "Could not save", "bad");
		}
	}
	async function discard() {
		if (await confirm({ title: "Discard changes?", text: "The chain goes back to the last saved version.", confirmLabel: "Discard", danger: true })) ctx.navigate("virtual-models");
	}
	async function remove() {
		const granted = users.filter((u) => u.grants.some((g) => g.virtualModel === draft.id));
		if (await confirm({ title: `Delete neoproxy/${draft.name}?`, text: granted.length ? `${plural(granted.length, "user")} will lose it. Requests for it start failing with an unknown-model error.` : "Requests for it start failing with an unknown-model error.", confirmLabel: "Delete", danger: true })) {
			await neo.deleteVirtualModel(draft.id);
			toast(`Deleted neoproxy/${draft.name}`);
			ctx.navigate("virtual-models");
		}
	}

	setDirty(isNew);
	renderRail();
	renderInspector();
	runSim();
}
