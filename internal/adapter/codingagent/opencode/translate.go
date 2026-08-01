package opencode

import (
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// translateNative converts a successfully parsed native opencode event
// (opencode v1.x `--format json` schema) onto the normalized Protocol-v1 event
// set (spec §12.4):
//
//   - text        → message.delta carrying the assistant chunk (Role
//     "assistant", MessageID correlating the step's parts);
//   - step_finish → usage.updated carrying the step's token accounting and
//     USD cost (PROVIDER_REPORTED confidence — the figures are
//     provider-observed, never EXACT; mapUsage re-checks this downstream);
//   - step_start  → consumed without a normalized counterpart.
//
// It deliberately does NOT synthesize run.completed from step_finish: the
// terminal event still comes from exit-code synthesis in run.go (KF-09 / I.9).
//
// recognised=false means the native type is not one this adapter knows; the
// caller keeps the standard warning+artifact behaviour for the line.
// emit=false (with recognised=true) means the line was understood but carries
// no normalized event — it is consumed, never counted as malformed.
func translateNative(ne NativeEvent) (ev protocol.NormalizedEvent, emit, recognised bool) {
	switch ne.Type {
	case nativeTypeStepStart:
		return protocol.NormalizedEvent{}, false, true
	case nativeTypeText:
		if ne.Part.Text == "" {
			return protocol.NormalizedEvent{}, false, true
		}
		return protocol.NormalizedEvent{
			Type:      protocol.EventMessageDelta,
			Timestamp: nativeEventTime(ne),
			Message: &protocol.MessagePayload{
				MessageID: ne.Part.MessageID,
				Delta:     ne.Part.Text,
				Role:      "assistant",
			},
		}, true, true
	case nativeTypeStepFinish:
		return protocol.NormalizedEvent{
			Type:      protocol.EventUsageUpdated,
			Timestamp: nativeEventTime(ne),
			Usage: &protocol.UsagePayload{
				InputTokens:      ne.Part.Tokens.Input,
				OutputTokens:     ne.Part.Tokens.Output,
				CacheReadTokens:  ne.Part.Tokens.Cache.Read,
				CacheWriteTokens: ne.Part.Tokens.Cache.Write,
				Cost:             ne.Part.Cost,
				Currency:         "USD",
				Confidence:       protocol.QuotaConfProviderReported,
			},
		}, true, true
	}
	return protocol.NormalizedEvent{}, false, false
}

// nativeEventTime converts the native millisecond timestamp, falling back to
// the local wall clock when the engine did not supply one.
func nativeEventTime(ne NativeEvent) time.Time {
	if ne.Timestamp > 0 {
		return time.UnixMilli(ne.Timestamp)
	}
	return time.Now()
}
