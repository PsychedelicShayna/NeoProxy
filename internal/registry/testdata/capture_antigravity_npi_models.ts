// Exports NeoPi's OWN google-antigravity model rows as the NeoPiModel
// records the Go registry embeds and diffs against:
//
//   bun capture_antigravity_npi_models.ts > ../models/antigravity_neopi.json
//
// The output is getBundledModels("google-antigravity"): the rows an account
// serves until its fetchAvailableModels discovery answers, and the rows the
// model manager merges a live roster over.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");

const COMPAT_KEYS = [
	"supportsFunctionPartId",
	"requiresSkipThoughtSignature",
	"requiresSkipThoughtSignatureOnFirstFunctionCall",
	"dropUnsignedThinking",
	"ccaLegacyParametersSchema",
	"multimodalFunctionResponse",
	"flashStreamLeakWorkaround",
	"claudeThinkingBetaHeader",
	"antigravityClaudeToolMode",
] as const;

/** One NeoPi Model as the Go NeoPiModel record (plus `kind`, which only discovery reads). */
export const neoPiRecord = (model: any) => {
	const compat = model.compat ?? {};
	const thinking = model.thinking;
	return {
		id: model.id,
		name: model.name,
		api: model.api,
		provider: model.provider,
		baseUrl: model.baseUrl,
		reasoning: model.reasoning,
		input: model.input,
		contextWindow: model.contextWindow ?? 0,
		maxTokens: model.maxTokens ?? 0,
		...(model.requestModelId ? { requestModelId: model.requestModelId } : {}),
		...(model.kind && model.kind !== "chat" ? { kind: model.kind } : {}),
		...(thinking
			? {
					thinking: {
						efforts: thinking.efforts,
						mode: thinking.mode,
						...(thinking.effortMap ? { effortMap: thinking.effortMap } : {}),
						...(thinking.requiresEffort ? { requiresEffort: true } : {}),
						...(thinking.suppressWhenOff ? { suppressWhenOff: true } : {}),
						...(thinking.unverified ? { unverified: true } : {}),
						...(thinking.supportsDisplay ? { supportsDisplay: true } : {}),
						...(thinking.effortRouting ? { effortRouting: thinking.effortRouting } : {}),
						...(thinking.effortBudgets ? { effortBudgets: thinking.effortBudgets } : {}),
						...(thinking.defaultLevel ? { defaultLevel: thinking.defaultLevel } : {}),
					},
				}
			: {}),
		geminiCompat: {
			identityClass: model.identity.class,
			...Object.fromEntries(COMPAT_KEYS.map(key => [key, compat[key] === true])),
			...(compat.streamFirstEventTimeoutMs !== undefined
				? { streamFirstEventTimeoutMs: compat.streamFirstEventTimeoutMs }
				: {}),
			...(compat.antigravityUsageLabel !== undefined ? { antigravityUsageLabel: compat.antigravityUsageLabel } : {}),
			...(compat.maxImageDimension !== undefined ? { maxImageDimension: compat.maxImageDimension } : {}),
			...(compat.maxImagePayloadBytes !== undefined ? { maxImagePayloadBytes: compat.maxImagePayloadBytes } : {}),
		},
		...(model.antigravityWireProfiles ? { antigravityWireProfiles: model.antigravityWireProfiles } : {}),
	};
};

if (import.meta.main) {
	const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
	console.log(
		JSON.stringify(
			{
				source: "NeoPi OWN getBundledModels(google-antigravity)",
				models: getBundledModels("google-antigravity").map(neoPiRecord),
			},
			null,
			1,
		),
	);
}
