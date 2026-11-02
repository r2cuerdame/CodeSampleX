package measurement

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func (s RetrievalQuality) MarshalJSON() ([]byte, error) {
	type plain RetrievalQuality
	if !s.CacheBytesUnavailable {
		return json.Marshal(plain(s))
	}
	return json.Marshal(struct {
		plain
		CacheBytes *int64 `json:"cacheBytes"`
	}{plain: plain(s)})
}
func (s *RetrievalQuality) UnmarshalJSON(raw []byte) error {
	type plain RetrievalQuality
	var value plain
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var field struct {
		CacheBytes json.RawMessage `json:"cacheBytes"`
	}
	if err := json.Unmarshal(raw, &field); err != nil {
		return err
	}
	value.CacheBytesUnavailable = len(field.CacheBytes) == 0 || bytes.Equal(bytes.TrimSpace(field.CacheBytes), []byte("null"))
	*s = RetrievalQuality(value)
	return nil
}
func (s RetrievalQuality) CacheHumanMB() string {
	if s.CacheBytesUnavailable {
		return "— (unavailable)"
	}
	return fmt.Sprintf("%.1f MB", float64(s.CacheBytes)/(1<<20))
}
