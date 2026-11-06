package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// DisableImageGenerationMode is a four-state config value for disable-image-generation.
//
// It supports:
//   - false: enabled
//   - true: disabled everywhere (including /v1/images/* endpoints)
//   - "chat": disabled for all non-images endpoints, but enabled for /v1/images/generations and /v1/images/edits
//   - "passthrough": never inject and never strip image_generation on non-images endpoints
//     (the client payload is forwarded unchanged); on /v1/images/* endpoints behave like "chat"
type DisableImageGenerationMode int

const (
	DisableImageGenerationOff DisableImageGenerationMode = iota
	DisableImageGenerationAll
	DisableImageGenerationChat
	DisableImageGenerationPassthrough
)

func (m DisableImageGenerationMode) String() string {
	switch m {
	case DisableImageGenerationOff:
		return "false"
	case DisableImageGenerationAll:
		return "true"
	case DisableImageGenerationChat:
		return "chat"
	case DisableImageGenerationPassthrough:
		return "passthrough"
	default:
		return "false"
	}
}

func (m DisableImageGenerationMode) MarshalJSON() ([]byte, error) {
	switch m {
	case DisableImageGenerationAll:
		return []byte("true"), nil
	case DisableImageGenerationChat:
		return json.Marshal("chat")
	case DisableImageGenerationPassthrough:
		return json.Marshal("passthrough")
	default:
		return []byte("false"), nil
	}
}

func (m *DisableImageGenerationMode) UnmarshalJSON(data []byte) error {
	mode, err := parseDisableImageGenerationJSON(data)
	if err != nil {
		return err
	}
	*m = mode
	return nil
}

func parseDisableImageGenerationJSON(data []byte) (DisableImageGenerationMode, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return DisableImageGenerationOff, nil
	}

	// bool
	var b bool
	if err := json.Unmarshal(trimmed, &b); err == nil {
		if b {
			return DisableImageGenerationAll, nil
		}
		return DisableImageGenerationOff, nil
	}

	// string
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return DisableImageGenerationOff, fmt.Errorf("invalid disable-image-generation value")
	}
	return parseDisableImageGenerationString(s)
}

func parseDisableImageGenerationString(s string) (DisableImageGenerationMode, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "", "false", "0", "off", "no":
		return DisableImageGenerationOff, nil
	case "true", "1", "on", "yes":
		return DisableImageGenerationAll, nil
	case "chat":
		return DisableImageGenerationChat, nil
	case "passthrough":
		return DisableImageGenerationPassthrough, nil
	default:
		return DisableImageGenerationOff, fmt.Errorf("invalid disable-image-generation value %q (allowed: true, false, chat, passthrough)", s)
	}
}
