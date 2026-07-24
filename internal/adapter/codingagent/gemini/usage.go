package gemini

import (
	"encoding/json"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// geminiResponse models the JSON document emitted by
// `gemini -p … --output-format json`. Every field is optional and tolerant of
// unknown future fields (additive change — ignored, not fatal). The exact key
// names mirror the Gemini CLI / GenerateContent usage metadata. When the CLI
// changes its shape, parseStream's document-mode fallback degrades gracefully:
// missing fields are reported as zero, never fabricated (spec §36.10).
type geminiResponse struct {
	Response *geminiResponseText `json:"response"`
	Text     string              `json:"text"` // fallback: some builds inline text.
	Usage    *geminiUsage        `json:"usage"`
	Session  *geminiSession      `json:"session"`
}

type geminiResponseText struct {
	Text string `json:"text"`
}

type geminiSession struct {
	ID string `json:"id"`
}

// geminiUsage carries the token accounting. The Gemini CLI nests counts under
// "metadata"; some builds may also expose top-level counts, so both are modelled
// and merged (metadata wins on conflict).
type geminiUsage struct {
	Metadata *geminiTokenMetadata `json:"metadata"`
	// Top-level fallbacks (older/alternate shapes).
	PromptTokenCount        *int64 `json:"promptTokenCount"`
	CandidatesTokenCount    *int64 `json:"candidatesTokenCount"`
	TotalTokenCount         *int64 `json:"totalTokenCount"`
	CachedContentTokenCount *int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      *int64 `json:"thoughtsTokenCount"`
	ToolUseTokenCount       *int64 `json:"toolUseTokenCount"`
}

// geminiTokenMetadata is the canonical GenerateContent usageMetadata shape.
type geminiTokenMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
	ToolUseTokenCount       int64 `json:"toolUseTokenCount"`
}

// mergeMetadata combines nested-metadata counts with top-level fallback counts,
// preferring nested metadata when present. Returns the resolved counts plus a
// flag indicating whether any count was actually reported.
func (u *geminiUsage) mergeMetadata() (geminiTokenMetadata, bool) {
	var m geminiTokenMetadata
	any := false
	if u == nil {
		return m, false
	}
	if u.Metadata != nil {
		m = *u.Metadata
		any = true
	}
	// Overlay top-level fallbacks only when the nested value is zero, so the
	// canonical shape always wins.
	if u.PromptTokenCount != nil {
		if m.PromptTokenCount == 0 {
			m.PromptTokenCount = *u.PromptTokenCount
		}
		any = true
	}
	if u.CandidatesTokenCount != nil {
		if m.CandidatesTokenCount == 0 {
			m.CandidatesTokenCount = *u.CandidatesTokenCount
		}
		any = true
	}
	if u.TotalTokenCount == nil {
		// keep as-is
	} else {
		if m.TotalTokenCount == 0 {
			m.TotalTokenCount = *u.TotalTokenCount
		}
		any = true
	}
	if u.CachedContentTokenCount != nil {
		if m.CachedContentTokenCount == 0 {
			m.CachedContentTokenCount = *u.CachedContentTokenCount
		}
		any = true
	}
	if u.ThoughtsTokenCount != nil {
		if m.ThoughtsTokenCount == 0 {
			m.ThoughtsTokenCount = *u.ThoughtsTokenCount
		}
		any = true
	}
	if u.ToolUseTokenCount != nil {
		if m.ToolUseTokenCount == 0 {
			m.ToolUseTokenCount = *u.ToolUseTokenCount
		}
		any = true
	}
	return m, any
}

// mapUsage translates Gemini's reported token counts onto a protocol
// [protocol.UsagePayload].
//
// Mapping (the protocol UsagePayload has no dedicated thought/tool fields, so
// those counts are folded into the closest semantic bucket without
// double-counting and never fabricated — spec §22, §36.10):
//
//   - InputTokens     ← promptTokenCount
//   - OutputTokens    ← candidatesTokenCount (the model's generated output)
//   - CacheReadTokens ← cachedContentTokenCount
//
// thoughtsTokenCount and toolUseTokenCount have no dedicated protocol field.
// They are NOT summed into OutputTokens because the Gemini API may already
// include them in candidatesTokenCount; summing would risk double-counting, so
// we omit them rather than overstate precision (§36.10). totalTokenCount is
// available for internal validation but has no UsagePayload field.
//
// Confidence is PROVIDER_REPORTED: the counts come directly from the provider's
// usage metadata. Cost is not reported by the CLI, so it stays zero (omitted)
// rather than estimated.
func mapUsage(m geminiTokenMetadata) protocol.UsagePayload {
	return protocol.UsagePayload{
		InputTokens:     m.PromptTokenCount,
		OutputTokens:    m.CandidatesTokenCount,
		CacheReadTokens: m.CachedContentTokenCount,
		Confidence:      protocol.QuotaConfProviderReported,
	}
}

// hasUsage reports whether a parsed response carries any usage counts.
func (r *geminiResponse) hasUsage() bool {
	if r == nil || r.Usage == nil {
		return false
	}
	_, any := r.Usage.mergeMetadata()
	return any
}

// decodeGeminiResponse parses raw bytes as a Gemini JSON response document. It
// tolerates unknown fields (ignored) and returns ok=false on a decode failure
// (handled by the caller as a malformed warning, never fatal).
func decodeGeminiResponse(raw []byte) (geminiResponse, bool) {
	var r geminiResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, false
	}
	return r, true
}

// responseText extracts the model's textual response from the document, checking
// the canonical "response.text" location first and the inline "text" fallback.
func (r *geminiResponse) responseText() string {
	if r == nil {
		return ""
	}
	if r.Response != nil && r.Response.Text != "" {
		return r.Response.Text
	}
	return r.Text
}

// sessionID extracts the reported session id, if any.
func (r *geminiResponse) sessionID() string {
	if r == nil || r.Session == nil {
		return ""
	}
	return r.Session.ID
}
