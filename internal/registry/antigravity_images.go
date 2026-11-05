package registry

import "slices"

// antigravityImageKind is the catalog kind NeoPi's rules give the Antigravity
// image SKUs (`models "gemini-3-pro-image" "gemini-3.1-flash-image" { kind
// "image" }`): chat discovery lists them beside chat models with no output
// modality, so only the kind tells them apart.
const antigravityImageKind = "image"

// AntigravityImageModelInfo is one NeoPi google-antigravity image record as an
// Images API model. NeoPi runs these through generateAntigravityImage, so the
// proxy serves them on the OpenAI image endpoints and never as chat.
func AntigravityImageModelInfo(model *NeoPiModel) *ModelInfo {
	info := AntigravityModelInfo(model)
	if info == nil {
		return nil
	}
	info.Type = OpenAIImageModelType
	info.SupportedOutputModalities = []string{"image"}
	info.Thinking = nil
	return info
}

// AntigravityBundledImageNeoPiModel resolves an id against NeoPi's bundled
// google-antigravity image rows.
func AntigravityBundledImageNeoPiModel(id string) *NeoPiModel {
	for i := range antigravitySeedCatalog {
		row := &antigravitySeedCatalog[i]
		if row.ID == id && row.Kind == antigravityImageKind {
			return row.NeoPiModel.Clone()
		}
	}
	return nil
}

// antigravityImageInfos is the image rows of a resolved roster as Images API
// models, in roster order.
func antigravityImageInfos(models []antigravityModel) []*ModelInfo {
	var out []*ModelInfo
	for i := range models {
		if models[i].Kind != antigravityImageKind {
			continue
		}
		out = append(out, AntigravityImageModelInfo(antigravityRecord(&models[i])))
	}
	return slices.Clip(out)
}
