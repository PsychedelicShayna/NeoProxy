package config

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/credentialweight"
)

// MaxCredentialWeight is the largest positive credential routing weight.
const MaxCredentialWeight = int(credentialweight.Max)

// ValidateCredentialWeight validates one optional config credential weight.
func ValidateCredentialWeight(weight *int) error {
	if weight == nil {
		return nil
	}
	_, errNormalize := credentialweight.Normalize(int64(*weight))
	return errNormalize
}

func validateCredentialWeightMap(root map[string]any) error {
	validate := func(values any, path string) error {
		items, _ := values.([]any)
		for i, raw := range items {
			item, _ := raw.(map[string]any)
			if value, present := item["weight"]; present {
				var weight int64
				switch n := value.(type) {
				case int:
					weight = int64(n)
				case int64:
					weight = n
				default:
					return fmt.Errorf("%s[%d].weight: weight must be an integer", path, i)
				}
				if _, err := credentialweight.Normalize(weight); err != nil {
					return fmt.Errorf("%s[%d].weight: %w", path, i, err)
				}
			}
		}
		return nil
	}
	for _, family := range v8KeyFamilies {
		if family.old == "openai-compatibility" {
			providers, _ := root[family.old].([]any)
			for i, raw := range providers {
				provider, _ := raw.(map[string]any)
				if err := validate(provider["api-key-entries"], fmt.Sprintf("openai-compatibility[%d].api-key-entries", i)); err != nil {
					return err
				}
			}
		} else {
			if err := validate(root[family.old], family.old); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateCredentialWeights validates weights for every API-key family.
func (cfg *Config) ValidateCredentialWeights() error {
	if cfg == nil {
		return nil
	}
	for index := range cfg.GeminiKey {
		if errValidate := ValidateCredentialWeight(cfg.GeminiKey[index].Weight); errValidate != nil {
			return fmt.Errorf("gemini-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for index := range cfg.InteractionsKey {
		if errValidate := ValidateCredentialWeight(cfg.InteractionsKey[index].Weight); errValidate != nil {
			return fmt.Errorf("interactions-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for index := range cfg.ClaudeKey {
		if errValidate := ValidateCredentialWeight(cfg.ClaudeKey[index].Weight); errValidate != nil {
			return fmt.Errorf("claude-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for index := range cfg.VertexCompatAPIKey {
		if errValidate := ValidateCredentialWeight(cfg.VertexCompatAPIKey[index].Weight); errValidate != nil {
			return fmt.Errorf("vertex-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for index := range cfg.CodexKey {
		if errValidate := ValidateCredentialWeight(cfg.CodexKey[index].Weight); errValidate != nil {
			return fmt.Errorf("codex-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for index := range cfg.XAIKey {
		if errValidate := ValidateCredentialWeight(cfg.XAIKey[index].Weight); errValidate != nil {
			return fmt.Errorf("xai-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for index := range cfg.MetaKey {
		if errValidate := ValidateCredentialWeight(cfg.MetaKey[index].Weight); errValidate != nil {
			return fmt.Errorf("meta-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for index := range cfg.GitLabDuoAgentKey {
		if errValidate := ValidateCredentialWeight(cfg.GitLabDuoAgentKey[index].Weight); errValidate != nil {
			return fmt.Errorf("gitlab-duo-agent-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for providerIndex := range cfg.OpenAICompatibility {
		for keyIndex := range cfg.OpenAICompatibility[providerIndex].APIKeyEntries {
			weight := cfg.OpenAICompatibility[providerIndex].APIKeyEntries[keyIndex].Weight
			if errValidate := ValidateCredentialWeight(weight); errValidate != nil {
				return fmt.Errorf("openai-compatibility[%d].api-key-entries[%d].weight: %w", providerIndex, keyIndex, errValidate)
			}
		}
	}
	return nil
}
