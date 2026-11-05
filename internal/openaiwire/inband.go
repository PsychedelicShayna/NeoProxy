package openaiwire

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// InBandError is a throttle or overload a gateway reported inside an HTTP 200
// stream, as plain text (`data: 429 Too Many Requests`, an nginx page) or a
// JSON-encoded string. NeoPi classifies it as a retryable provider error
// (createInBandProviderErrorFromText, body-error.ts).
type InBandError struct {
	// Status is the HTTP status the text names, 429 when it names none.
	Status  int
	Message string
}

func (e *InBandError) Error() string { return e.Message }

const (
	maxInBandDetailChars   = 4096
	inBandDetailPlaceholer = "Provider returned an in-band provider error"
)

var (
	inBandRetryableText = regexp.MustCompile(`(?i)\brate.?limit|too many requests|too\s+many\s+concurren|service.{0,20}unavailable|temporarily\s+unavailable|server.?error|internal.?error|overloaded|capacity|throttl|retry\s+(?:your\s+)?request|please\s+retry`)
	inBandLeadingStatus = regexp.MustCompile(`(?i)^\s*(?:HTTP[/.]\d(?:\.\d)?\s+)?([45]\d{2})(?:\b|$)`)
	inBandMarkup        = regexp.MustCompile(`<[^>]*>`)
	inBandControl       = regexp.MustCompile(`[\x00-\x1f\x7f]+`)
	inBandSpace         = regexp.MustCompile(`\s+`)
)

// classifyInBandText is createInBandProviderErrorFromText: nil when the text is
// not recognizable as a throttle.
func classifyInBandText(text string) *InBandError {
	detail := inBandMarkup.ReplaceAllString(text, " ")
	detail = inBandControl.ReplaceAllString(detail, " ")
	detail = strings.TrimSpace(inBandSpace.ReplaceAllString(detail, " "))
	if detail == "" || !inBandRetryableText.MatchString(detail) {
		return nil
	}
	if len(detail) > maxInBandDetailChars {
		detail = detail[:maxInBandDetailChars]
	}
	status := 0
	if match := inBandLeadingStatus.FindStringSubmatch(detail); match != nil {
		status, _ = strconv.Atoi(match[1])
	}
	if status == 429 || status >= 500 && status <= 599 {
		message := detail
		if lead := inBandLeadingStatus.FindStringSubmatch(detail); lead == nil || lead[1] != strconv.Itoa(status) {
			message = fmt.Sprintf("%d %s", status, detail)
		}
		return &InBandError{Status: status, Message: message}
	}
	return &InBandError{Status: 429, Message: detail}
}
