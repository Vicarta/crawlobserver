package config

import (
	"encoding/json"
	"strings"
	"time"
)

// SessionStopMetadata is stored in the crawl session config snapshot so stop
// reasons can be exposed without a ClickHouse schema migration.
type SessionStopMetadata struct {
	Reason  string    `json:"reason,omitempty"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

// SessionFinalizationMetadata records durable causes of a completed session
// with errors without requiring a crawl_sessions schema change.
type SessionFinalizationMetadata struct {
	PageRankFailure string `json:"pagerank_failure,omitempty"`
	BufferFailure   string `json:"buffer_failure,omitempty"`
	LostPages       int64  `json:"lost_pages,omitempty"`
	LostLinks       int64  `json:"lost_links,omitempty"`
}

// WithSessionFinalizationMetadata enriches the saved session config with its
// terminal PageRank and buffer outcomes.
func WithSessionFinalizationMetadata(raw string, meta SessionFinalizationMetadata) string {
	meta.PageRankFailure = strings.TrimSpace(meta.PageRankFailure)
	meta.BufferFailure = strings.TrimSpace(meta.BufferFailure)
	if meta.PageRankFailure == "" && meta.BufferFailure == "" && meta.LostPages == 0 && meta.LostLinks == 0 {
		return raw
	}
	data := map[string]interface{}{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &data)
	}
	data["Finalization"] = meta
	out, err := json.Marshal(data)
	if err != nil {
		return raw
	}
	return string(out)
}

// SessionFinalizationMetadataFromJSON extracts the terminal finalization
// outcome from the stored session config.
func SessionFinalizationMetadataFromJSON(raw string) (SessionFinalizationMetadata, bool) {
	var data map[string]json.RawMessage
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &data) != nil {
		return SessionFinalizationMetadata{}, false
	}
	var meta SessionFinalizationMetadata
	if json.Unmarshal(data["Finalization"], &meta) != nil {
		return SessionFinalizationMetadata{}, false
	}
	if meta.PageRankFailure == "" && meta.BufferFailure == "" && meta.LostPages == 0 && meta.LostLinks == 0 {
		return SessionFinalizationMetadata{}, false
	}
	return meta, true
}

// WithSessionStopMetadata returns a config JSON blob enriched with stop metadata.
func WithSessionStopMetadata(raw string, meta SessionStopMetadata) string {
	meta.Reason = strings.TrimSpace(meta.Reason)
	meta.Message = strings.TrimSpace(meta.Message)
	if meta.Reason == "" && meta.Message == "" {
		return raw
	}
	if meta.At.IsZero() {
		meta.At = time.Now().UTC()
	} else {
		meta.At = meta.At.UTC()
	}

	data := map[string]interface{}{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &data)
	}
	data["Stop"] = map[string]interface{}{
		"reason":  meta.Reason,
		"message": meta.Message,
		"at":      meta.At.Format(time.RFC3339),
	}
	out, err := json.Marshal(data)
	if err != nil {
		return raw
	}
	return string(out)
}

// SessionStopMetadataFromJSON extracts stop metadata from stored session config.
func SessionStopMetadataFromJSON(raw string) (SessionStopMetadata, bool) {
	var data map[string]interface{}
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &data) != nil {
		return SessionStopMetadata{}, false
	}

	stop, ok := data["Stop"].(map[string]interface{})
	if !ok {
		return SessionStopMetadata{}, false
	}

	meta := SessionStopMetadata{}
	if v, ok := stop["reason"].(string); ok {
		meta.Reason = strings.TrimSpace(v)
	}
	if v, ok := stop["message"].(string); ok {
		meta.Message = strings.TrimSpace(v)
	}
	if v, ok := stop["at"].(string); ok && strings.TrimSpace(v) != "" {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(v)); err == nil {
			meta.At = t
		}
	}
	if meta.Reason == "" && meta.Message == "" {
		return SessionStopMetadata{}, false
	}
	return meta, true
}
