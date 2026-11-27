package aieditor

import "encoding/json"

// Missing counts stay absent; zero is a real provider-reported value. Output
// includes reasoning where the protocol reports it. Detail fields are subsets,
// except tool prompt tokens, which Gemini reports separately from its prompt.
type Usage struct {
	PromptTokens        *int   `json:"prompt_tokens,omitempty"`
	CompletionTokens    *int   `json:"completion_tokens,omitempty"`
	TotalTokens         *int   `json:"total_tokens,omitempty"`
	ReasoningTokens     *int   `json:"reasoning_tokens,omitempty"`
	CachedTokens        *int   `json:"cached_tokens,omitempty"`
	CacheCreationTokens *int   `json:"cache_creation_tokens,omitempty"`
	ToolPromptTokens    *int   `json:"tool_prompt_tokens,omitempty"`
	TotalSource         string `json:"total_source,omitempty"`
}

const maxSafeTokens = 1<<53 - 1

type tokenFields map[string]json.RawMessage

func usageFields(raw json.RawMessage) tokenFields {
	var fields tokenFields
	_ = json.Unmarshal(raw, &fields)
	return fields
}

func (fields tokenFields) count(name string) *int {
	raw := fields[name]
	var value int
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &value) != nil || value < 0 || int64(value) > maxSafeTokens {
		return nil
	}
	return &value
}

func tokenSum(required *int, optional ...*int) *int {
	if required == nil {
		return nil
	}
	n := *required
	for _, value := range optional {
		if value != nil {
			if int64(n)+int64(*value) > maxSafeTokens {
				return nil
			}
			n += *value
		}
	}
	return &n
}

// Providers send cumulative snapshots, sometimes with only changed fields.
// Keep the protocol's raw counters so cache/thought components are never added
// twice and a later zero is not mistaken for a missing update.
type usageTracker struct {
	value         Usage
	input, output *int
}

func (u *usageTracker) update(raw json.RawMessage, protocol string) bool {
	f := usageFields(raw)
	changed, totalsChanged := false, false
	set := func(dst **int, value *int) {
		if value != nil && (*dst == nil || **dst != *value) {
			*dst, changed = value, true
		}
	}
	setTotalComponent := func(dst **int, value *int) {
		if value != nil && (*dst == nil || **dst != *value) {
			*dst, changed, totalsChanged = value, true, true
		}
	}
	total := f.count("total_tokens")
	switch protocol {
	case ProtocolResponses, ProtocolAzureResponses:
		setTotalComponent(&u.value.PromptTokens, f.count("input_tokens"))
		setTotalComponent(&u.value.CompletionTokens, f.count("output_tokens"))
		set(&u.value.CachedTokens, usageFields(f["input_tokens_details"]).count("cached_tokens"))
		set(&u.value.ReasoningTokens, usageFields(f["output_tokens_details"]).count("reasoning_tokens"))
	case ProtocolAnthropic:
		set(&u.input, f.count("input_tokens"))
		set(&u.value.CachedTokens, f.count("cache_read_input_tokens"))
		set(&u.value.CacheCreationTokens, f.count("cache_creation_input_tokens"))
		setTotalComponent(&u.value.PromptTokens, tokenSum(u.input, u.value.CachedTokens, u.value.CacheCreationTokens))
		setTotalComponent(&u.value.CompletionTokens, f.count("output_tokens"))
	case ProtocolGeminiText:
		setTotalComponent(&u.value.PromptTokens, f.count("promptTokenCount"))
		set(&u.output, f.count("candidatesTokenCount"))
		set(&u.value.ReasoningTokens, f.count("thoughtsTokenCount"))
		setTotalComponent(&u.value.CompletionTokens, tokenSum(u.output, u.value.ReasoningTokens))
		set(&u.value.CachedTokens, f.count("cachedContentTokenCount"))
		setTotalComponent(&u.value.ToolPromptTokens, f.count("toolUsePromptTokenCount"))
		total = f.count("totalTokenCount")
	default:
		setTotalComponent(&u.value.PromptTokens, f.count("prompt_tokens"))
		setTotalComponent(&u.value.CompletionTokens, f.count("completion_tokens"))
		set(&u.value.ReasoningTokens, usageFields(f["completion_tokens_details"]).count("reasoning_tokens"))
		cached := usageFields(f["prompt_tokens_details"]).count("cached_tokens")
		if cached == nil {
			cached = f.count("prompt_cache_hit_tokens")
		}
		set(&u.value.CachedTokens, cached)
	}
	if total != nil {
		set(&u.value.TotalTokens, total)
		if u.value.TotalSource != "reported" {
			u.value.TotalSource, changed = "reported", true
		}
	} else if totalsChanged {
		// Never keep an earlier total beside newer input/output counters.
		u.value.TotalTokens, u.value.TotalSource = nil, ""
		if u.value.PromptTokens != nil && u.value.CompletionTokens != nil {
			u.value.TotalTokens = tokenSum(u.value.PromptTokens, u.value.CompletionTokens, u.value.ToolPromptTokens)
			if u.value.TotalTokens != nil {
				u.value.TotalSource = "calculated"
			}
		}
	}
	return changed
}
