package antigravity

import (
	"fmt"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// CredentialFileName returns the filename used to persist Antigravity
// credentials: the account email when there is one.
func CredentialFileName(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return "antigravity.json"
	}
	return fmt.Sprintf("antigravity-%s.json", email)
}

// BuildAuth assembles the persisted credential for a completed sign-in. The
// metadata keys are the ones the provider has always written (type,
// access_token, refresh_token, expired, project_id, email, timestamp), so
// existing auth files and NeoPi imports keep working.
func BuildAuth(credential *Credential, now time.Time) *coreauth.Auth {
	if credential == nil {
		return nil
	}
	email := strings.TrimSpace(credential.Email)
	projectID := strings.TrimSpace(credential.ProjectID)
	metadata := map[string]any{
		"type":          "antigravity",
		"access_token":  credential.Access,
		"refresh_token": credential.Refresh,
		"timestamp":     now.UnixMilli(),
	}
	if !credential.ExpiresAt.IsZero() {
		metadata["expired"] = credential.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if email != "" {
		metadata["email"] = email
	}
	if projectID != "" {
		metadata["project_id"] = projectID
	}
	if accountID := strings.TrimSpace(credential.AccountID); accountID != "" {
		metadata["account_id"] = accountID
	}

	fileName := CredentialFileName(email)
	label := email
	if label == "" {
		label = "antigravity"
	}
	auth := &coreauth.Auth{
		ID:         fileName,
		Provider:   "antigravity",
		FileName:   fileName,
		Label:      label,
		Status:     coreauth.StatusActive,
		Metadata:   metadata,
		Attributes: map[string]string{},
	}
	if email != "" {
		auth.Attributes["email"] = email
	}
	if projectID != "" {
		auth.Attributes["project_id"] = projectID
	}
	return auth
}

// CredentialFromAuth projects an engine auth's stored metadata back into the
// lifecycle credential.
func CredentialFromAuth(a *coreauth.Auth) *Credential {
	if a == nil {
		return nil
	}
	text := func(key string) string {
		value, _ := a.Metadata[key].(string)
		if value == "" {
			value = a.Attributes[key]
		}
		return strings.TrimSpace(value)
	}
	credential := &Credential{
		Access:    text("access_token"),
		Refresh:   text("refresh_token"),
		ProjectID: text("project_id"),
		Email:     text("email"),
		AccountID: text("account_id"),
	}
	if expired := text("expired"); expired != "" {
		if parsed, err := time.Parse(time.RFC3339, expired); err == nil {
			credential.ExpiresAt = parsed
		}
	}
	return credential
}
