package executor

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/tidwall/gjson"
)

// NeoPi always asks Messages for SSE, including non-streaming downstream turns.
// Dialect translators consume that SSE directly; native Messages callers need
// its message_start, content deltas and final usage folded into one Message.
func neoPiAggregateMessage(transcript []byte) ([]byte, error) {
	var message []byte
	var blocks []json.RawMessage
	partialJSON := make(map[int][]byte)
	stopped := false
	for _, line := range bytes.Split(transcript, []byte{'\n'}) {
		data := neoPiSSEData(line)
		if len(data) == 0 {
			continue
		}
		event := gjson.ParseBytes(data)
		index := int(event.Get("index").Int())
		switch event.Get("type").String() {
		case "message_start":
			message = []byte(event.Get("message").Raw)
		case "content_block_start":
			if index < 0 || index > len(blocks) {
				return nil, fmt.Errorf("claude: out-of-order content block %d", index)
			}
			block := json.RawMessage(event.Get("content_block").Raw)
			if index == len(blocks) {
				blocks = append(blocks, block)
			} else {
				blocks[index] = block
			}
		case "content_block_delta":
			if index < 0 || index >= len(blocks) {
				return nil, fmt.Errorf("claude: delta for unopened content block %d", index)
			}
			delta := event.Get("delta")
			switch delta.Get("type").String() {
			case "text_delta":
				blocks[index] = neoPiSet(blocks[index], "text", gjson.GetBytes(blocks[index], "text").String()+delta.Get("text").String())
				if signature := delta.Get("textSignature"); signature.Type == gjson.String {
					blocks[index] = neoPiSet(blocks[index], "textSignature", signature.String())
				}
			case "thinking_delta":
				blocks[index] = neoPiSet(blocks[index], "thinking", gjson.GetBytes(blocks[index], "thinking").String()+delta.Get("thinking").String())
			case "signature_delta":
				blocks[index] = neoPiSet(blocks[index], "signature", gjson.GetBytes(blocks[index], "signature").String()+delta.Get("signature").String())
			case "input_json_delta":
				partialJSON[index] = append(partialJSON[index], delta.Get("partial_json").String()...)
			case "citations_delta":
				blocks[index] = neoPiSetRaw(blocks[index], "citations.-1", []byte(delta.Get("citation").Raw))
			}
		case "content_block_stop":
			if input := partialJSON[index]; len(input) > 0 {
				if !gjson.ValidBytes(input) {
					return nil, fmt.Errorf("claude: malformed tool input in block %d", index)
				}
				blocks[index] = neoPiSetRaw(blocks[index], "input", input)
				delete(partialJSON, index)
			}
		case "message_delta":
			event.Get("delta").ForEach(func(key, value gjson.Result) bool {
				message = neoPiSetRaw(message, key.String(), []byte(value.Raw))
				return true
			})
			event.Get("usage").ForEach(func(key, value gjson.Result) bool {
				message = neoPiSetRaw(message, "usage."+key.String(), []byte(value.Raw))
				return true
			})
		case "message_stop":
			stopped = true
		}
	}
	if len(message) == 0 || !stopped {
		return nil, fmt.Errorf("claude: incomplete message envelope")
	}
	return neoPiSet(message, "content", blocks), nil
}
