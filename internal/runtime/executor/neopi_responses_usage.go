package executor

import (
	"math"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// neoPiResponsesUsage reads a terminal Responses event's usage as NeoPi's
// populateResponsesUsageFromResponse does (openai-shared.ts): orchestration
// tokens are a separate bucket, and the primary totals already include them
// when the reported total sits closer to them than to primary plus
// orchestration. A report that excludes them adds them to the billed buckets.
func neoPiResponsesUsage(event []byte) (usage.Detail, bool) {
	detail, ok := helps.ParseCodexUsage(event)
	if !ok {
		return detail, ok
	}
	node := gjson.GetBytes(event, "response.usage")
	orchInput := node.Get("input_tokens_details.orchestration_input_tokens").Int()
	orchCached := node.Get("input_tokens_details.orchestration_input_cached_tokens").Int()
	orchOutput := node.Get("output_tokens_details.orchestration_output_tokens").Int()
	if orchInput+orchOutput <= 0 && orchCached <= 0 {
		return detail, ok
	}
	reportedIn, reportedOut := node.Get("input_tokens").Int(), node.Get("output_tokens").Int()
	total := node.Get("total_tokens")
	primary := float64(reportedIn + reportedOut)
	separate := primary + float64(orchInput+orchOutput)
	includes := total.Exists() && total.Type == gjson.Number && orchInput+orchOutput > 0 &&
		math.Abs(total.Float()-primary) <= math.Abs(total.Float()-separate)
	if includes {
		return detail, ok
	}
	orchCached = min(orchInput, orchCached)
	detail.InputTokens += orchInput
	detail.OutputTokens += orchOutput
	detail.CachedTokens += orchCached
	detail.CacheReadTokens += orchCached
	if total.Exists() && total.Type == gjson.Number {
		detail.TotalTokens = total.Int()
	} else {
		detail.TotalTokens = detail.InputTokens + detail.OutputTokens
	}
	detail.TokenBreakdown = usage.NewSubsetTokenBreakdown(detail.InputTokens, detail.CacheReadTokens, detail.CacheCreationTokens, detail.OutputTokens, detail.ReasoningTokens, detail.TotalTokens)
	return detail, ok
}

// restoreResponsesSourceFields puts back the request fields NeoPi's stream
// options carry and the Codex-shaped translation drops: sampling, the output
// limit and an explicit reasoning summary (including null).
func restoreResponsesSourceFields(source, body []byte) []byte {
	root := gjson.ParseBytes(source)
	for _, path := range []string{"temperature", "top_p", "max_output_tokens"} {
		if value := root.Get(path); value.Type == gjson.Number && !gjson.GetBytes(body, path).Exists() {
			if next, err := sjson.SetRawBytes(body, path, []byte(value.Raw)); err == nil {
				body = next
			}
		}
	}
	if summary := root.Get("reasoning.summary"); summary.Exists() && (summary.Type == gjson.Null || summary.Type == gjson.String) {
		if next, err := sjson.SetRawBytes(body, "reasoning.summary", []byte(summary.Raw)); err == nil {
			body = next
		}
	}
	return body
}
