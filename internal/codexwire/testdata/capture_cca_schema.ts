// Runs NeoPi's OWN toolWireSchema, then both Antigravity schema pipelines over a
// corpus reaching every branch of the Cloud Code Assist option set:
//   claude: normalizeSchemaForCCA(toolWireSchema(tool))
//   gemini: normalizeSchemaForCCA(normalizeSchemaForGoogle(toolWireSchema(tool)))
// (google-shared.ts / normalizeAntigravityTools in google-gemini-cli.ts). The
// Google-only result is recorded too, so the shared walker is checked for both
// option sets. Every result is JSON.stringify'd: key order is wire order.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { toolWireSchema } = await import(root + "/pi-ai/src/utils/schema/wire.ts");
const { normalizeSchemaForGoogle, normalizeSchemaForCCA } = await import(root + "/pi-ai/src/utils/schema/normalize.ts");

const str = { type: "string" };
const obj = (properties: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({ type: "object", properties, ...extra });

const schemas: Record<string, unknown> = {
	// --- baseline -----------------------------------------------------------
	"simple-object": obj({ path: { type: "string", description: "File." }, limit: { type: "integer" } }, { required: ["path"] }),
	"empty-root": {},
	"root-no-type": { properties: { a: str } },
	"root-string": { type: "string", description: "Just text." },
	"root-array": { type: "array", items: { type: "number" } },
	"empty-objects": obj({ cfg: { type: "object" }, nested: obj({ one: { type: "boolean" } }), deep: { type: "object", properties: {} } }),
	"all-scalar-types": obj({ s: str, n: { type: "number" }, i: { type: "integer" }, b: { type: "boolean" }, a: { type: "array", items: str }, o: { type: "object" } }),

	// --- type arrays ---------------------------------------------------------
	"type-array-nullable": obj({ limit: { type: ["number", "null"], description: "Max hits." }, term: str }, { required: ["term", "limit"] }),
	"type-array-multi": obj({ value: { type: ["string", "array", "null"], items: str, minLength: 2, maxItems: 3 } }),
	"type-array-multi-no-null": obj({ value: { type: ["string", "number"], description: "Either." } }),
	"type-array-three-constrained": obj({ v: { type: ["string", "integer", "boolean"], minLength: 1, maximum: 9, pattern: "^x" } }),
	"type-array-only-null": obj({ v: { type: ["null"] } }),
	"type-array-empty": obj({ v: { type: [] } }),
	"type-array-nonstring": obj({ v: { type: [1, 2] } }),
	"type-array-duplicate": obj({ v: { type: ["string", "string", "null"] } }),
	"type-array-with-anyof": obj({ v: { type: ["string", "null"], anyOf: [{ type: "string" }, { type: "number" }] } }),
	"type-array-object-array": obj({ v: { type: ["object", "array"], properties: { a: str }, items: str, required: ["a"] } }),
	"type-array-with-const": obj({ v: { type: ["string"], const: "x" } }),
	"type-array-root": { type: ["object", "null"], properties: { a: str } },
	"type-array-empty-with-enum": obj({ v: { type: [], enum: ["a"] } }),
	"type-null-property": obj({ nothing: { type: "null", description: "Nada." } }),

	// --- nullable ------------------------------------------------------------
	"nullable-keyword": obj({ a: { type: "string", nullable: true }, b: { type: "number", nullable: false } }, { required: ["a", "b"] }),
	"nullable-keyword-invalid": obj({ a: { type: "string", nullable: "yes" } }),
	"anyof-null-scalar": obj({ maybe: { anyOf: [{ type: "string", maxLength: 5 }, { type: "null" }] } }, { required: ["maybe"] }),
	"anyof-null-two-scalars": obj({ many: { anyOf: [{ type: "string" }, { type: "integer" }, { type: "null" }] } }),
	"anyof-null-enum-untyped": obj({ pick: { anyOf: [{ enum: ["a", "b"] }, { type: "null" }], description: "Pick." } }, { required: ["pick", "other"], additionalProperties: false }),
	"oneof-null-enum-untyped": obj({ pick: { oneOf: [{ enum: [1, 2] }, { type: "null" }] }, keep: str }, { required: ["pick", "keep"] }),
	"anyof-null-object": obj({ cfg: { anyOf: [obj({ a: str }), { type: "null" }] } }, { required: ["cfg"] }),
	"anyof-null-with-extra-null-keys": obj({ v: { anyOf: [{ enum: ["x"] }, { type: "null", description: "no" }] } }),
	"anyof-only-null": obj({ v: { anyOf: [{ type: "null" }] } }),
	"anyof-nested-nullable-property": obj({ outer: obj({ inner: { anyOf: [{ enum: ["q"] }, { type: "null" }] } }, { required: ["inner"] }) }, { required: ["outer"] }),
	"anyof-null-conflicting-keys": obj({ v: { description: "parent", anyOf: [{ enum: ["x"], description: "child" }, { type: "null" }] } }),
	"required-with-nonstrings": obj({ a: str, b: { anyOf: [{ enum: [1] }, { type: "null" }] } }, { required: ["a", 7, "b", "a"] }),

	// --- const / enum --------------------------------------------------------
	"const-anyof": obj({ mode: { anyOf: [{ const: "a" }, { const: "b" }, { const: "a" }], description: "Mode." } }),
	"const-anyof-typed": obj({ mode: { oneOf: [{ const: "a", type: "string" }, { const: "b", type: "string" }] } }),
	"const-mixed-null": obj({ pick: { oneOf: [{ const: 1 }, { const: null }] } }),
	"const-mixed-types": obj({ pick: { anyOf: [{ const: 1 }, { const: "x" }] } }),
	"const-anyof-extra-keys": obj({ pick: { anyOf: [{ const: "a" }, { const: "b" }], default: "a", pattern: "x", title: "T", nullable: true } }),
	"const-anyof-and-oneof": obj({ pick: { anyOf: [{ const: "a" }, { const: "b" }], oneOf: [{ type: "string" }, { type: "number" }] } }),
	"bare-const": obj({ fixed: { const: "x" }, num: { const: 3 }, flag: { const: true }, nil: { const: null } }),
	"const-object-array": obj({ o: { const: { anyOf: [1], nullable: true } }, a: { const: [1, 2] } }),
	"const-and-enum": obj({ v: { enum: ["a", "b"], const: "c" }, w: { type: "string", enum: ["a"], const: "a" } }),
	"bare-enum": obj({ size: { enum: ["s", "m"] }, level: { enum: [1, 2] }, mixed: { type: "string", enum: ["a", 2] }, flags: { enum: [true, false] } }),
	"bare-enum-mixed-no-type": obj({ v: { enum: ["a", 1] }, w: { enum: [null, "a"] }, x: { enum: [] } }),
	"enum-duplicates": obj({ v: { type: "string", enum: ["a", "a"] } }),
	"enum-objects": obj({ v: { enum: [{ type: "null" }, { anyOf: [] }] } }),
	"enum-numbers-strings": obj({ v: { enum: [1, "1"] } }),

	// --- combiner collapsing -------------------------------------------------
	"anyof-mixed-scalars": obj({ v: { anyOf: [{ type: "string" }, { type: "number" }], description: "Either." } }),
	"anyof-mixed-descriptions": obj({ v: { anyOf: [{ type: "string", description: "a" }, { type: "integer", description: "b" }, { type: "boolean", description: "a" }] } }),
	"anyof-mixed-conflicting-title": obj({ v: { anyOf: [{ type: "string", title: "A" }, { type: "integer", title: "B" }] } }),
	"anyof-mixed-spill-descriptions": obj({ v: { anyOf: [{ type: "string", minLength: 1, pattern: "^a" }, { type: "integer", minimum: 2 }] } }),
	"anyof-mixed-defaults": obj({ v: { anyOf: [{ type: "string", default: "x" }, { type: "number", default: 2 }] } }),
	"anyof-mixed-description-nonstring": obj({ v: { anyOf: [{ type: "string", description: 5 }, { type: "number", description: "x" }] } }),
	"anyof-mixed-description-nonstring-incoming": obj({ v: { anyOf: [{ type: "string", description: "a" }, { type: "number", description: 7 }] } }),
	"anyof-mixed-description-empty": obj({ v: { anyOf: [{ type: "string", description: "" }, { type: "number", description: "b" }] } }),
	"anyof-mixed-description-empty-incoming": obj({ v: { anyOf: [{ type: "string", description: "a" }, { type: "number", description: "" }] } }),
	"anyof-mixed-title-equal": obj({ v: { anyOf: [{ type: "string", title: "T" }, { type: "number", title: "T" }] } }),
	"oneof-mixed-scalars": obj({ v: { oneOf: [{ type: "boolean" }, { type: "integer" }, { type: "string" }] } }),
	"anyof-mixed-with-null": obj({ v: { anyOf: [{ type: "null" }, { type: "string" }, { type: "number" }] } }),
	"anyof-mixed-array-string": obj({ v: { anyOf: [{ type: "array", items: str }, { type: "string" }] } }),
	"anyof-mixed-string-array": obj({ v: { anyOf: [{ type: "string" }, { type: "array", items: str }] } }),
	"anyof-mixed-object-string": obj({ v: { anyOf: [obj({ a: str }), { type: "string" }] } }),
	"anyof-mixed-parent-siblings": obj({ v: { description: "Parent.", default: 3, anyOf: [{ type: "string" }, { type: "number" }] } }),
	"anyof-mixed-parent-foreign-key": obj({ v: { items: { type: "string" }, anyOf: [{ type: "string" }, { type: "number" }] } }),
	"anyof-duplicate-types": obj({ v: { anyOf: [{ type: "string" }, { type: "string", description: "x" }, { type: "number" }] } }),
	"anyof-same-type-enums": obj({ v: { anyOf: [{ type: "string", enum: ["a"] }, { type: "string", enum: ["b", "a"] }] } }),
	"anyof-same-type-enums-conflict": obj({ v: { anyOf: [{ type: "string", enum: ["a"], description: "x" }, { type: "string", enum: ["b"] }] } }),
	"anyof-same-type-enum-and-plain": obj({ v: { anyOf: [{ type: "string", enum: ["a"], description: "narrow" }, { type: "string", description: "broad" }] } }),
	"anyof-same-type-plain": obj({ v: { anyOf: [{ type: "string", description: "first" }, { type: "string", minLength: 3 }] } }),
	"anyof-same-type-objects-keep": obj({ v: { anyOf: [obj({ a: str }), obj({ a: str })] } }),
	"anyof-single-variant": obj({ v: { anyOf: [{ type: "string", description: "only" }] } }),
	"anyof-empty-variant": obj({ v: { anyOf: [{}, { type: "string" }] } }),
	"anyof-empty-array": obj({ v: { anyOf: [] } }),
	"anyof-nested": obj({ v: { anyOf: [{ anyOf: [{ type: "string" }, { type: "number" }] }, { type: "boolean" }] } }),
	"anyof-nested-null": obj({ v: { anyOf: [{ anyOf: [{ type: "string" }, { type: "null" }] }, { type: "number" }] } }),
	"oneof-and-anyof-both": obj({ v: { anyOf: [{ type: "string" }, { type: "number" }], oneOf: [{ type: "boolean" }, { type: "integer" }] } }),
	"anyof-unknown-type": obj({ v: { anyOf: [{ type: "string" }, { type: "weird" }] } }),
	"anyof-items-array": obj({ list: { type: "array", items: { anyOf: [{ type: "string" }, { type: "integer" }] } } }),
	"anyof-ref-variants": { type: "object", properties: { v: { anyOf: [{ $ref: "#/$defs/A" }, { $ref: "#/$defs/B" }] } }, $defs: { A: { type: "string" }, B: { type: "number" } } },
	"anyof-ref-same-twice": { type: "object", properties: { v: { anyOf: [{ $ref: "#/$defs/A" }, { $ref: "#/$defs/A" }] }, w: { $ref: "#/$defs/A" } }, $defs: { A: { type: "string", enum: ["x"] } } },

	// --- object combiner merging --------------------------------------------
	"anyof-objects-merge": { anyOf: [obj({ a: str }, { required: ["a"] }), obj({ b: { type: "number" } }, { required: ["b"] })] },
	"anyof-objects-shared-required": { anyOf: [obj({ a: str, c: str }, { required: ["a", "c"] }), obj({ b: { type: "number" }, c: str }, { required: ["b", "c"] })], description: "Union." },
	"oneof-objects-merge": obj({ cfg: { oneOf: [obj({ x: str }, { required: ["x"] }), obj({ y: str }, { required: ["y"] })] } }),
	"anyof-objects-property-conflict-types": { anyOf: [obj({ v: str }), obj({ v: { type: "number" } })] },
	"anyof-objects-property-conflict-three": { anyOf: [obj({ v: str }), obj({ v: { type: "number" } }), obj({ v: { type: "boolean" } })] },
	"anyof-objects-property-conflict-enums": { anyOf: [obj({ k: { type: "string", enum: ["a"] } }), obj({ k: { type: "string", enum: ["b"] } })] },
	"anyof-objects-property-conflict-complex": { anyOf: [obj({ v: obj({ a: str }) }), obj({ v: { type: "array", items: str } })] },
	"anyof-objects-property-same": { anyOf: [obj({ v: str, a: str }), obj({ v: str, b: str })] },
	"anyof-objects-parent-properties": obj({ base: str, shared: str }, { required: ["base"], anyOf: [obj({ shared: { type: "number" } , a: str }, { required: ["a"] }), obj({ b: str })] }),
	"anyof-objects-parent-required-missing": obj({ base: str }, { required: ["base", "ghost"], anyOf: [obj({ a: str }), obj({ b: str })] }),
	"anyof-objects-required-not-in-props": { anyOf: [obj({ a: str }, { required: ["a", "zzz"] }), obj({ a: str }, { required: ["a", "zzz"] })] },
	"anyof-objects-no-type-shape": { anyOf: [{ properties: { a: str }, required: ["a"] }, { properties: { b: str } }] },
	"anyof-objects-additional-only": { anyOf: [{ additionalProperties: true }, { type: "object", additionalProperties: false }] },
	"anyof-objects-with-scalar": { anyOf: [obj({ a: str }), { type: "string" }] },
	"anyof-objects-bad-required": { anyOf: [{ type: "object", properties: { a: str }, required: "a" }, obj({ b: str })] },
	"anyof-objects-bad-properties": { anyOf: [{ type: "object", properties: [1] }, obj({ b: str })] },
	"anyof-objects-discriminated": { oneOf: [obj({ kind: { const: "a" }, x: str }, { required: ["kind", "x"] }), obj({ kind: { const: "b" }, y: str }, { required: ["kind", "y"] })] },
	"anyof-objects-nested-merge": obj({ opts: { anyOf: [obj({ inner: { anyOf: [obj({ a: str }), obj({ b: str })] } }), obj({ other: str })] } }),
	"anyof-objects-with-nullable-props": { anyOf: [obj({ a: { anyOf: [{ enum: ["x"] }, { type: "null" }] } }, { required: ["a"] }), obj({ b: str })] },
	"anyof-objects-numeric-keys": { anyOf: [obj({ b: str }), obj({ "2": str, "1": str })] },
	"numeric-property-names": obj({ b: str, "10": str, "2": str, a: str }, { required: ["b", "10"] }),
	"allof-objects": { allOf: [obj({ a: str }, { required: ["a"] }), obj({ b: str })] },
	"allof-scalar": obj({ v: { allOf: [{ type: "string" }, { minLength: 1 }] } }),
	"allof-empty": obj({ v: { allOf: [] } }),

	// --- refs / defs ---------------------------------------------------------
	"refs": { type: "object", properties: { node: { $ref: "#/$defs/Node", description: "Root node." }, list: { type: "array", items: { $ref: "#/definitions/Leaf" } } }, $defs: { Node: { type: "object", properties: { child: { $ref: "#/$defs/Node" }, name: str } } }, definitions: { Leaf: { type: "string", enum: ["x", "y"] } } },
	"refs-recursive-array": { type: "object", properties: { tree: { $ref: "#/$defs/Tree" } }, $defs: { Tree: { type: "object", properties: { kids: { type: "array", items: { $ref: "#/$defs/Tree" } }, v: str } } } },
	"refs-mutual": { type: "object", properties: { a: { $ref: "#/$defs/A" } }, $defs: { A: { type: "object", properties: { b: { $ref: "#/$defs/B" } } }, B: { type: "object", properties: { a: { $ref: "#/$defs/A" } } } } },
	"refs-sibling-keywords": { type: "object", properties: { v: { $ref: "#/$defs/S", description: "Sibling.", default: "z" } }, $defs: { S: { type: "string", minLength: 2 } } },
	"refs-unresolved": obj({ ext: { $ref: "https://example.com/schema.json" }, missing: { $ref: "#/$defs/Nope" }, deep: { $ref: "#/$defs/A/properties/x" } }, { $defs: { A: obj({ x: str }) } }),
	"refs-root": obj({ self: { $ref: "#" } }),
	"refs-in-anyof-null": { type: "object", properties: { v: { anyOf: [{ $ref: "#/$defs/E" }, { type: "null" }] } }, required: ["v"], $defs: { E: { enum: ["a", "b"] } } },
	"refs-escaped-name": { type: "object", properties: { v: { $ref: "#/$defs/a~1b" } }, $defs: { "a/b": { type: "boolean" } } },
	"defs-unused": obj({ a: str }, { $defs: { Unused: { type: "number" } }, definitions: { Old: { type: "string" } } }),
	"refs-nested-defs-keyword": { type: "object", properties: { v: { type: "object", properties: { w: { $ref: "#/$defs/W" } }, $defs: { Inner: str } } }, $defs: { W: { type: "integer" } } },

	// --- draft / structure ---------------------------------------------------
	"draft7-items-tuple": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { pair: { type: "array", items: [{ type: "string" }, { type: "number" }] }, tags: { type: "array", items: str, uniqueItems: true } } },
	"draft7-items-empty-tuple": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { t: { type: "array", items: [] } } },
	"draft7-additional-items": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { t: { type: "array", items: [str], additionalItems: { type: "number" } } } },
	"draft7-dependencies": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { a: str, b: str }, dependencies: { a: ["b"], b: obj({ c: str }) } },
	"draft7-definitions": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { v: { $ref: "#/definitions/V" } }, definitions: { V: { type: "string" } } },
	"draft2020-prefix-items": { $schema: "https://json-schema.org/draft/2020-12/schema", type: "object", properties: { t: { type: "array", prefixItems: [str, { type: "number" }], items: false } } },
	"boolean-subschemas": obj({ anything: true, never: false, arr: { type: "array", items: true } }, { additionalProperties: false }),
	"boolean-subschema-combiners": obj({ v: { anyOf: [true, { type: "string" }] }, w: { not: false } }),
	"boolean-additional-properties-schema": obj({ a: str }, { additionalProperties: { type: "string" } }),
	"snake-case": obj({ opts: { type: "object", additional_properties: false, any_of: [obj({ a: str })] }, t: { type: "array", prefix_items: [str] } }),
	"snake-case-collision": obj({ opts: { type: "object", additional_properties: false, additionalProperties: true, property_ordering: ["a"], properties: { a: str } } }),
	"property-ordering-present": obj({ a: str, b: str }, { propertyOrdering: ["b", "a"] }),
	"property-named-keywords": obj({ const: str, type: str, enum: str, nullable: str, not: str, properties: str, anyOf: str, $ref: str, items: str, default: str, required: str }),
	"property-named-type-object": obj({ type: obj({ a: str }), properties: obj({ x: str }) }),
	"property-named-nullable-required": obj({ nullable: str, a: str }, { required: ["nullable", "a"] }),
	"property-named-spilled": obj({ format: str, pattern: str, minimum: { type: "number" }, examples: str, default: str, additionalProperties: str, $schema: str }),
	"nested-deep": obj({ l1: obj({ l2: obj({ l3: obj({ l4: obj({ l5: obj({ l6: obj({ leaf: { anyOf: [{ enum: ["z"] }, { type: "null" }] } }, { required: ["leaf"] }) }) }) }) }) }) }),
	"items-nested-arrays": obj({ grid: { type: "array", items: { type: "array", items: { type: ["number", "null"] } } } }),

	// --- spill / unsupported / metadata -------------------------------------
	"spill": obj({ id: { type: "string", pattern: "^[a-z]+$", format: "uuid", default: "abc", examples: ["x"], $comment: "c" }, n: { type: "integer", minimum: 1, maximum: 9, description: "Count." } }),
	"spill-all-liftable": obj({ v: { type: "array", items: str, minItems: 1, maxItems: 4, uniqueItems: true, minProperties: 1, maxProperties: 2, exclusiveMinimum: 0, exclusiveMaximum: 10, multipleOf: 0.5, minLength: 1, maxLength: 9 } }),
	"spill-existing-description": obj({ v: { type: "string", description: "Base.", format: "date" } }),
	"spill-numbers": obj({ v: { type: "number", default: 1e21, minimum: 0.1, maximum: 100, multipleOf: 1e-7, exclusiveMinimum: 1.5e300 } }),
	"spill-unicode": obj({ v: { type: "string", default: "caf\u00e9 \u2028 \ud83d\ude00 \"q\" \\ \n", description: "d\u2029" } }),
	"spill-nested-default": obj({ v: { type: "object", default: { a: [1, { b: null }], c: "d" } } }),
	"spill-on-combiner-node": obj({ v: { anyOf: [{ type: "string" }, { type: "number" }], default: "x", format: "f" } }),
	"unsupported-non-liftable": obj({ a: str }, { $id: "x", $anchor: "y", $dynamicRef: "z", $dynamicAnchor: "w", patternProperties: { "^a": str }, propertyNames: { pattern: "^a" }, unevaluatedProperties: false, dependentSchemas: { a: obj({ b: str }) }, dependentRequired: { a: ["b"] }, contains: str, minContains: 1, maxContains: 2, contentEncoding: "base64", contentMediaType: "x/y", contentSchema: str }),
	"mcp-annotations": obj({ a: { type: "string", "x-mcp-header": "Region", deprecated: true, readOnly: true, writeOnly: false, $comment: "note", title: "A" } }, { title: "Tool", "x-vendor": { k: 1 } }),
	"unknown-keywords": obj({ a: { type: "string", "x-custom": [1, 2], foo: { anyOf: [] } } }),
	"title-description-defaults": obj({ a: { type: "string", title: "T", description: "D", default: "x" } }),
	"annotation-wrong-type": obj({ a: { type: "string", deprecated: "yes" } }),
	"if-then-else": obj({ a: str }, { if: { properties: { a: { const: "x" } } }, then: { required: ["a"] }, else: {} }),
	"not-enum": obj({ s: { type: "string", not: { enum: [1] } } }),
	"not-const": obj({ t: { type: "string", not: { const: "z" } } }),
	"not-type": obj({ t: { not: { type: "string" } } }),
	"not-false-root": { not: false },

	// --- validation failures -> fallback ------------------------------------
	"invalid-required-duplicates": obj({ a: str }, { required: ["a", "a"] }),
	"invalid-required-string": obj({ a: str }, { required: "a" }),
	"invalid-required-nonstring-no-props": { type: "object", required: ["a", 1] },
	"invalid-enum-empty": obj({ v: { type: "string", enum: [] } }),
	"invalid-enum-not-array": obj({ v: { type: "string", enum: "a" } }),
	"invalid-enum-duplicate-numbers": obj({ v: { enum: [1, 1.0, 2] } }),
	"invalid-type-name": obj({ v: { type: "str" } }),
	"invalid-type-number": obj({ v: { type: 5 } }),
	"invalid-min-properties-negative": obj({ v: { type: "object", minProperties: -1 } }),
	"invalid-min-properties-float": obj({ v: { type: "object", minProperties: 1.5 } }),
	"valid-min-properties": obj({ v: { type: "object", minProperties: 1, maxProperties: 3 } }),
	"invalid-properties-array": { type: "object", properties: [] },
	"invalid-properties-nonschema-value": { type: "object", properties: { a: 5 } },
	"invalid-properties-null-value": { type: "object", properties: { a: null } },
	"invalid-items-null": obj({ v: { type: "array", items: null } }),
	"invalid-nested-schema": obj({ v: { type: "array", items: { type: "nope" } } }),
	"invalid-additional-items-survives": obj({ v: { type: "array", items: str } }, { definitions: 3 }),
	"invalid-description-non-string": obj({ v: { type: "string", description: 5 } }),
	"invalid-if-string": obj({ a: str }, { if: "x" }),
	"invalid-then-array": obj({ a: str }, { then: [] }),
};

const results: Array<Record<string, unknown>> = [];
for (const [name, parameters] of Object.entries(schemas)) {
	try {
		const wire = toolWireSchema({ name, description: "", parameters } as any);
		const googleWire = normalizeSchemaForGoogle(wire);
		results.push({
			name,
			parameters,
			wire: JSON.stringify(wire),
			google: JSON.stringify(googleWire),
			claude: JSON.stringify(normalizeSchemaForCCA(wire)),
			gemini: JSON.stringify(normalizeSchemaForCCA(googleWire)),
		});
	} catch (error) {
		throw new Error(`case ${name}: ${error instanceof Error ? error.stack : error}`);
	}
}
console.log(JSON.stringify({ source: "NeoPi OWN toolWireSchema + normalizeSchemaForGoogle + normalizeSchemaForCCA", cases: results }, null, 2));
