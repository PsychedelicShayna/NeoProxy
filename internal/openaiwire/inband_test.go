package openaiwire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func scriptedTurn(body string, attempts *int) *Turn {
	return &Turn{
		Request: &Request{URL: "http://upstream.invalid/responses", Header: http.Header{}, Body: []byte(`{}`)},
		Wait:    func(context.Context, time.Duration) error { return nil },
		Do: func(*http.Request) (*http.Response, error) {
			*attempts++
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		},
	}
}

// A throttle a gateway wrote into a 200 stream is a retryable provider error
// with the status it names, as NeoPi's decodeStream classifies it.
func TestInBandThrottleFramesAreClassifiedAndRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		status int
	}{
		{"plain 429", "data: 429 Too Many Requests\n\n", 429},
		{"html 503", "data: <html><body>503 Service Unavailable</body></html>\n\n", 503},
		{"json string", "data: \"429 rate limit exceeded\"\n\n", 429},
		{"no status", "data: Please retry your request later\n\n", 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			err := scriptedTurn(tc.body, &attempts).Run(t.Context(), func([]byte) error { return nil })
			var inBand *InBandError
			if !errors.As(err, &inBand) || inBand.Status != tc.status {
				t.Fatalf("err = %v, want in-band status %d", err, tc.status)
			}
			if attempts != 2 {
				t.Errorf("attempts = %d, want one transient retry (2)", attempts)
			}
		})
	}
}

// Text that is not a throttle keeps failing loudly, and a JSON string that is
// not one is dropped as no event.
func TestInBandNonThrottleText(t *testing.T) {
	attempts := 0
	err := scriptedTurn("data: totally malformed\n\n", &attempts).Run(t.Context(), func([]byte) error { return nil })
	var streamErr *StreamError
	if !errors.As(err, &streamErr) || attempts != 1 {
		t.Fatalf("err = %v attempts = %d", err, attempts)
	}
	attempts = 0
	events := 0
	err = scriptedTurn("data: \"hello\"\n\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n", &attempts).Run(t.Context(), func([]byte) error { events++; return nil })
	if err != nil || events != 1 {
		t.Fatalf("err = %v events = %d, want the string frame dropped and one event", err, events)
	}
}
