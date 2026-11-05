package antigravitywire

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

// ErrMalformedEvent is a `data:` frame that is not JSON (JSON.parse's
// SyntaxError in NeoPi's readSseJson).
var ErrMalformedEvent = errors.New("malformed SSE JSON event")

// SSEReader is readSseJson: it yields the JSON of each dispatched event,
// skips events with empty data and stops at the `[DONE]` sentinel. A
// container-shaped final event that is not strict JSON is a cut-off stream
// tail and ends iteration cleanly.
type SSEReader struct {
	reader *bufio.Reader
	data   []byte
	have   bool
	done   bool
}

// NewSSEReader wraps a response body.
func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{reader: bufio.NewReaderSize(r, 64*1024)}
}

// Next returns the next event's JSON, or io.EOF at the end of the stream.
func (s *SSEReader) Next() ([]byte, error) {
	for {
		if s.done {
			return nil, io.EOF
		}
		line, err := s.reader.ReadBytes('\n')
		trailing := false
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			if len(line) == 0 && !s.have {
				return nil, io.EOF
			}
			trailing = true
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			if event, ok, errEvent := s.dispatch(trailing); ok || errEvent != nil {
				return event, errEvent
			}
			if trailing {
				return nil, io.EOF
			}
			continue
		}
		if trailing {
			s.push(line)
			event, ok, errEvent := s.dispatch(true)
			if ok || errEvent != nil {
				return event, errEvent
			}
			return nil, io.EOF
		}
		s.push(line)
	}
}

func (s *SSEReader) push(line []byte) {
	if line[0] == ':' {
		return
	}
	name, value, _ := strings.Cut(string(line), ":")
	value = strings.TrimPrefix(value, " ")
	if name != "data" {
		return
	}
	if s.have {
		s.data = append(s.data, '\n')
	}
	s.data = append(s.data, value...)
	s.have = true
}

// dispatch ends the current event; ok reports whether it carries JSON.
func (s *SSEReader) dispatch(trailing bool) ([]byte, bool, error) {
	data := s.data
	s.data, s.have = nil, false
	if len(data) == 0 {
		return nil, false, nil
	}
	if string(data) == "[DONE]" {
		s.done = true
		return nil, false, io.EOF
	}
	if gjson.ValidBytes(data) {
		return data, true, nil
	}
	if trailing {
		if first := strings.TrimLeft(string(data), " \t\r\n"); strings.HasPrefix(first, "{") || strings.HasPrefix(first, "[") {
			s.done = true
			return nil, false, io.EOF
		}
	}
	return nil, false, ErrMalformedEvent
}
