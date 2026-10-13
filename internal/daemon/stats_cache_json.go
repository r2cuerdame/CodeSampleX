package daemon

import (
	"bytes"
	"encoding/json"
)

func (s Stats) MarshalJSON() ([]byte, error) {
	type plain Stats
	if !s.CacheBytesUnavailable {
		return json.Marshal(plain(s))
	}
	return json.Marshal(struct {
		plain
		CacheBytes *int64 `json:"cacheBytes"`
	}{plain: plain(s)})
}
func (s *Stats) UnmarshalJSON(raw []byte) error {
	type plain Stats
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
	if value.CacheBytesUnavailable {
		value.RetrievalQuality.CacheBytesUnavailable = true
	}
	*s = Stats(value)
	return nil
}
func statsCacheHuman(st Stats) string {
	if st.CacheBytesUnavailable {
		return "— (unavailable)"
	}
	return humanBytes(st.CacheBytes)
}

func (s Stats) CacheHumanMB() string {
	r := s.RetrievalQuality
	r.CacheBytes = s.CacheBytes
	r.CacheBytesUnavailable = s.CacheBytesUnavailable
	return r.CacheHumanMB()
}
