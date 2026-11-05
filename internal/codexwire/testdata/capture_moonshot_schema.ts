// Runs NeoPi's OWN normalizeSchemaForMoonshot over schemas covering every branch
// the Moonshot (MFJS) option set reaches, recording each JSON.stringify'd result:
// once over toolWireSchema(parameters) (the production path) and once directly
// over the unprocessed parameters.
import { dirname } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { toolWireSchema } = await import(root + "/pi-ai/src/utils/schema/wire.ts");
const { normalizeSchemaForMoonshot } = await import(root + "/pi-ai/src/utils/schema/normalize.ts");

const obj = (properties: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({ type: "object", properties, ...extra });

const schemas: Record<string, unknown> = {
	"bare-const-string": obj({ fixed: { const: "x" }, num: { const: 3 }, flag: { const: true } }),
	"bare-const-null-object": obj({ nothing: { const: null }, rec: { const: { a: 1 } }, list: { const: [1, 2] } }),
	"const-with-type": obj({ fixed: { type: "string", const: "x", description: "Fixed." } }),
	"const-with-enum": obj({ pick: { enum: ["a", "b"], const: "c" }, dup: { enum: ["a"], const: "a" } }),
	"const-anyof-strings": obj({ mode: { anyOf: [{ const: "a" }, { const: "b" }, { const: "a" }], description: "Mode." } }),
	"const-anyof-explicit-type": obj({ mode: { anyOf: [{ type: "string", const: "a" }, { type: "string", const: "b" }] } }),
	"const-anyof-mixed-explicit-type": obj({ mode: { anyOf: [{ type: "string", const: "a" }, { type: "number", const: 2 }] } }),
	"const-oneof-numbers": obj({ pick: { oneOf: [{ const: 1 }, { const: 2 }], title: "Pick", minimum: 1 } }),
	"const-oneof-mixed-null": obj({ pick: { oneOf: [{ const: 1 }, { const: null }] } }),
	"const-anyof-mixed-types": obj({ pick: { anyOf: [{ const: 1 }, { const: "s" }] } }),
	"const-anyof-with-siblings": obj({ pick: { anyOf: [{ const: "a" }, { const: "b" }], default: "a", nullable: true, pattern: "^a", description: "Pick." } }),
	"const-anyof-and-oneof": obj({ pick: { anyOf: [{ const: "a" }], oneOf: [{ type: "string" }, { type: "number" }] } }),
	"const-anyof-extra-key-const": obj({ pick: { const: "z", anyOf: [{ const: "a" }, { const: "b" }] } }),
	"anyof-partial-const": obj({ pick: { anyOf: [{ const: "a" }, { type: "string" }] } }),
	"anyof-const-object-variants": obj({ pick: { anyOf: [{ const: { k: 1 } }, { const: { k: 2 } }] } }),
	"oneof-residual": obj({ v: { oneOf: [{ type: "string" }, { type: "number", minimum: 0 }], description: "Either." } }),
	"oneof-with-anyof": obj({ v: { anyOf: [{ type: "string" }], oneOf: [{ type: "number" }, { type: "boolean" }] } }),
	"oneof-before-anyof-key-order": obj({ v: { oneOf: [{ type: "number" }], description: "d", anyOf: [{ type: "string" }] } }),
	"oneof-nested-in-items": obj({ list: { type: "array", items: { oneOf: [{ type: "string" }, { type: "object", properties: { a: { oneOf: [{ type: "integer" }] } } }] } } }),
	"type-array-nullable": obj({ limit: { type: ["number", "null"], description: "Max hits." }, term: { type: "string" } }, { required: ["term"] }),
	"type-array-null-first": obj({ limit: { type: ["null", "integer"], minimum: 1 } }),
	"type-array-only-null": obj({ nothing: { type: ["null"] }, also: { type: ["null", "null"] } }),
	"type-array-single": obj({ one: { type: ["string"], items: { type: "string" }, properties: { a: {} }, required: ["a"], description: "d" } }),
	"type-array-empty": obj({ none: { type: [], description: "d" } }),
	"type-array-multi": obj({ value: { type: ["string", "array", "null"], items: { type: "string" }, minLength: 2, maxItems: 3 } }),
	"type-array-multi-object": obj({ value: { type: ["object", "string"], properties: { a: { type: "string" } }, required: ["a"], additionalProperties: false, format: "uri", description: "Either." } }),
	"type-array-multi-numeric": obj({ n: { type: ["integer", "number", "integer"], minimum: 0, maximum: 5, multipleOf: 1 } }),
	"type-array-multi-with-anyof": obj({ v: { type: ["string", "number"], anyOf: [{ type: "string" }, { type: "number" }] } }),
	"type-array-multi-with-enum": obj({ v: { type: ["string", "number"], enum: ["a", 1], default: "a" } }),
	"type-array-non-string-members": obj({ v: { type: [1, "string"] }, w: { type: [1, 2] } }),
	"enum-without-type": obj({ size: { enum: ["s", "m"] }, level: { enum: [1, 2] }, nums: { enum: [1.5, 2] }, flag: { enum: [true, false] } }),
	"enum-mixed-no-type": obj({ mixed: { enum: ["a", 2] }, withNull: { enum: ["a", null] }, onlyNull: { enum: [null] }, empty: { enum: [] } }),
	"enum-with-type": obj({ mixed: { type: "string", enum: ["a", 2] }, bool: { type: "boolean", enum: [true] }, obj: { type: "object", enum: [{ a: 1 }] }, nulls: { type: "string", enum: ["a", null] } }),
	"enum-with-anyof-no-infer": obj({ v: { enum: ["a"], anyOf: [{ type: "string" }] }, w: { enum: ["a"], oneOf: [{ type: "string" }] } }),
	"enum-non-scalar-dropped": obj({ arr: { enum: [[1], [2]] }, o: { enum: [{ a: 1 }] }, b: { enum: [true] }, n: { enum: [null] } }),
	"enum-inside-anyof-branches": obj({ v: { anyOf: [{ enum: ["a", "b"] }, { enum: [1, 2] }, { enum: [null] }] } }),
	"null-type": obj({ nothing: { type: "null", description: "Nada." } }),
	"anyof-null": obj({ maybe: { anyOf: [{ type: "string", maxLength: 5 }, { type: "null" }] }, many: { anyOf: [{ type: "string" }, { type: "integer" }, { type: "null" }] } }),
	"nullable-keyword": obj({ a: { type: "string", nullable: true }, b: { nullable: false, type: "number" }, c: { anyOf: [{ type: "string", nullable: true }] } }),
	"property-named-nullable": obj({ nullable: { type: "boolean" }, title: { type: "string" }, format: { type: "string" } }),
	"spill-validation": obj({ id: { type: "string", pattern: "^[a-z]+$", format: "uuid", default: "abc", examples: ["x"], $comment: "c" }, n: { type: "integer", minimum: 1, maximum: 9, exclusiveMinimum: 0, exclusiveMaximum: 10, multipleOf: 2, description: "Count." } }),
	"spill-array-object": obj({ list: { type: "array", items: { type: "string" }, minItems: 1, maxItems: 5, uniqueItems: true }, rec: { type: "object", minProperties: 1, maxProperties: 3, properties: { a: { type: "string", minLength: 1, maxLength: 4 } } } }),
	"spill-existing-description": obj({ s: { type: "string", description: "Existing.", pattern: "^x", format: "email" }, empty: { type: "string", description: "", minLength: 2 }, numeric: { type: "string", description: 5, minLength: 2 } }),
	"spill-values-complex": obj({ s: { type: "string", pattern: "a\"b\\c\n\u00e9\u2028", examples: [{ k: [1, 2.5, null, true] }], format: "date-time" }, n: { type: "number", minimum: 1e21, maximum: -0.5, multipleOf: 0.1 } }),
	"stripped-no-spill": obj({ s: { type: "string", title: "T", $schema: "x", deprecated: true, readOnly: true, writeOnly: false, contentEncoding: "base64", contentMediaType: "text/plain", $dynamicRef: "#a", $dynamicAnchor: "a" } }),
	"stripped-structural-keywords": obj({ s: { type: "string", if: { minLength: 1 }, then: { pattern: "a" }, else: { pattern: "b" }, not: { const: "z" }, patternProperties: { "^a": { type: "string" } }, propertyNames: { pattern: "^a" }, contains: { type: "string" }, minContains: 1, maxContains: 2, unevaluatedProperties: false, unevaluatedItems: false, dependentRequired: { a: ["b"] }, dependentSchemas: { a: { type: "string" } } } }),
	"prefix-items-tuple": obj({ pair: { type: "array", prefixItems: [{ type: "string" }, { type: "number" }], items: false, minItems: 2 } }),
	"draft7-items-tuple": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { pair: { type: "array", items: [{ type: "string" }, { type: "number" }] }, tags: { type: "array", items: { type: "string" }, uniqueItems: true } } },
	"draft7-additional-items": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { pair: { type: "array", items: [{ type: "string" }], additionalItems: { type: "number" } } } },
	"draft7-nullable-and-const": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { a: { type: "string", const: "x" }, b: { type: ["string", "null"] } }, definitions: { Foo: { type: "string" } } },
	"boolean-subschemas": obj({ anything: true, never: false, arr: { type: "array", items: true } }, { additionalProperties: false }),
	"boolean-subschemas-combiners": obj({ v: { anyOf: [true, false, { type: "string" }] }, w: { allOf: [true, { type: "number" }] }, x: { oneOf: [false] } }),
	"boolean-keyword-slots": obj({ s: { type: "string", nullable: true, default: true }, e: { enum: [true, false] }, a: { type: "object", additionalProperties: true, unevaluatedProperties: false } }),
	"boolean-in-properties-map": obj({ a: true, b: false, c: { type: "object", properties: { d: true } } }),
	"boolean-items-false": obj({ list: { type: "array", items: false }, list2: { type: "array", items: true } }),
	"additional-properties-bool-and-schema": obj({ a: { type: "object", additionalProperties: true }, b: { type: "object", additionalProperties: false }, c: { type: "object", additionalProperties: { type: "string", pattern: "^x" } }, d: { type: "object", additionalProperties: { anyOf: [{ const: "a" }, { const: "b" }] } }, e: { type: "object", additionalProperties: {} } }),
	"default-and-description-preserved": obj({ a: { type: "string", default: "x", description: "D." }, b: { type: "integer", default: 3 }, c: { type: "object", default: { z: 1 }, properties: {} }, d: { type: "array", default: [], items: { type: "string", default: "q" } } }),
	"default-with-nested-schema-like": obj({ a: { type: "object", default: { type: ["string", "null"], const: 1, minLength: 2 } }, b: { const: "x", default: { enum: [1, null] } } }),
	"allof-kept": obj({ a: { allOf: [{ type: "object", properties: { x: { type: "string", minLength: 1 } } }, { required: ["x"] }] }, b: { allOf: [{ const: "a" }, { const: "b" }] } }),
	"anyof-allof-nested": obj({ a: { anyOf: [{ allOf: [{ type: "string", format: "uri" }] }, { type: ["number", "null"] }] } }),
	"refs": obj({ node: { $ref: "#/$defs/Node", description: "Root node." }, list: { type: "array", items: { $ref: "#/definitions/Leaf" } } }, { $defs: { Node: { type: "object", properties: { child: { $ref: "#/$defs/Node" }, name: { type: "string", minLength: 1 } } } }, definitions: { Leaf: { type: "string", enum: ["x", "y"] } } }),
	"refs-with-null-and-const": obj({ a: { $ref: "#/$defs/Mode" }, b: { anyOf: [{ $ref: "#/$defs/Mode" }, { type: "null" }] } }, { $defs: { Mode: { oneOf: [{ const: "fast" }, { const: "slow" }] } } }),
	"refs-unresolved": obj({ a: { $ref: "#/$defs/Missing" }, b: { $ref: "https://example.com/s.json" } }),
	"refs-ref-with-siblings": obj({ a: { $ref: "#/$defs/S", description: "Own.", minLength: 3, default: "x" } }, { $defs: { S: { type: "string", pattern: "^a", description: "Shared." } } }),
	"id-anchor-kept": { $id: "https://example.com/s.json", $anchor: "a", type: "object", properties: { a: { $id: "x", type: "string" } } },
	"empty-schema": {},
	"empty-properties": obj({}),
	"empty-object-property": obj({ cfg: { type: "object" }, nested: { type: "object", properties: { one: { type: "boolean" } } }, any: {} }),
	"title-everywhere": { title: "Root", type: "object", properties: { a: { title: "A", type: "string" } }, $defs: { X: { title: "X" } } },
	"property-named-keywords": obj({ const: { type: "string" }, type: { type: "string" }, enum: { type: "string" }, oneOf: { type: "string" }, anyOf: { type: "string" }, default: { type: "string" }, not: { type: "string" } }, { required: ["type"] }),
	"property-named-keywords-array-types": obj({ properties: { type: ["string", "null"] }, items: { type: ["integer", "null"] } }),
	"snake-case-untouched": obj({ opts: { type: "object", additional_properties: false, any_of: [{ type: "object", properties: { a: { type: "string" } } }], prefix_items: [{ type: "string" }] } }),
	"numeric-property-names-order": obj({ b: { type: "string" }, "2": { type: "string" }, a: { type: "string" }, "1": { type: "string" } }),
	"unicode-and-escapes": obj({ "é\"k": { type: "string", description: "Line\nbreak \u2028 and \"quote\".", format: "x\ty" } }),
	"deep-nesting": obj({ l1: obj({ l2: obj({ l3: obj({ l4: { type: "array", items: obj({ l5: { type: ["string", "null"], pattern: "^a", description: "Deep." }, l5b: { anyOf: [{ const: 1 }, { const: 2 }] } }, { required: ["l5"] }), minItems: 1 } }) }) }) }),
	"items-nested-arrays": obj({ grid: { type: "array", items: { type: "array", items: { type: ["number", "null"] }, minItems: 2 } } }),
	"mixed-realistic-tool": { type: "object", properties: { path: { type: "string", description: "File path." }, mode: { anyOf: [{ const: "r" }, { const: "w" }], description: "Open mode." }, limit: { type: ["integer", "null"], minimum: 1, description: "Max lines." }, filters: { type: "array", items: { oneOf: [{ type: "string", pattern: "^f" }, { type: "object", properties: { k: { type: "string" } }, additionalProperties: false }] }, maxItems: 10 }, opts: { type: "object", additionalProperties: { type: "boolean" }, default: {} } }, required: ["path"], additionalProperties: false },
	"root-type-array": { type: ["object", "null"], properties: { a: { type: "string" } }, required: ["a"] },
	"root-anyof": { anyOf: [{ type: "object", properties: { a: { type: "string" } } }, { type: "object", properties: { b: { type: "string" } } }] },
	"root-oneof-const": { oneOf: [{ const: "a" }, { const: "b" }] },
	"root-enum": { enum: ["a", "b"] },
	"deprecated-annotations": obj({ a: { type: "string", deprecated: true, readOnly: true, "x-mcp-header": "X-A", description: "d" } }),
	"dependencies-draft7": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { a: { type: "string" }, b: { type: "string" } }, dependencies: { a: ["b"], b: { properties: { c: { type: "string", minLength: 1 } } } } },
	"examples-and-default-array": obj({ tags: { type: "array", items: { type: "string" }, examples: [["a"]], default: ["a"] } }),
	"integer-vs-number-const": obj({ i: { const: 1 }, f: { const: 1.5 }, big: { const: 12345678901234567890 } }),
};

// Direct-only inputs that toolWireSchema does not accept as tool parameters.
const rawOnly: Record<string, unknown> = {
	"raw-boolean-true": true,
	"raw-boolean-false": false,
	"raw-array-root": [{ type: ["string", "null"] }, true],
	"raw-string-root": "x",
	"raw-null-root": null,
};

const cases: unknown[] = [];
for (const [name, parameters] of Object.entries(schemas)) {
	const wire = toolWireSchema({ name, description: "", parameters } as any);
	cases.push({
		name,
		parameters,
		wire: JSON.stringify(wire),
		moonshot: JSON.stringify(normalizeSchemaForMoonshot(structuredClone(wire))),
		raw: JSON.stringify(normalizeSchemaForMoonshot(structuredClone(parameters))),
	});
}
for (const [name, parameters] of Object.entries(rawOnly)) {
	cases.push({ name, parameters, raw: JSON.stringify(normalizeSchemaForMoonshot(structuredClone(parameters))) });
}

const out = dirname(import.meta.path) + "/moonshot_schema_capture.json";
await Bun.write(out, JSON.stringify({ source: "NeoPi OWN toolWireSchema + normalizeSchemaForMoonshot", cases }, null, 2) + "\n");
console.log(`wrote ${cases.length} cases to ${out}`);
