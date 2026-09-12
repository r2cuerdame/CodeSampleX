package domain

import (
	"strings"
	"testing"
)

func validPayload() PresencePayload {
	return PresencePayload{
		SchemaVersion: 1,
		ClientClass:   "ordinary",
		ClientVersion: "v0.1.44",
		Epoch1d:       "2026-09-12",
		Token1d:       strings.Repeat("a", 32),
		Epoch7d:       "2958",
		Token7d:       strings.Repeat("b", 32),
		Epoch30d:      "690",
		Token30d:      strings.Repeat("c", 32),
	}
}

func TestPresencePayloadValidateValid(t *testing.T) {
	p := validPayload()
	if err := p.Validate(); err != nil {
		t.Fatalf("valid payload failed validation: %v", err)
	}
}

func TestPresencePayloadValidateInvalidSchema(t *testing.T) {
	p := validPayload()
	p.SchemaVersion = 2
	if err := p.Validate(); err != ErrPresenceSchemaVersion {
		t.Errorf("got %v, want ErrPresenceSchemaVersion", err)
	}
}

func TestPresencePayloadValidateInvalidClientClass(t *testing.T) {
	for _, bad := range []string{"", "   ", "has spaces", "too$symbol", strings.Repeat("x", 65)} {
		p := validPayload()
		p.ClientClass = bad
		if err := p.Validate(); err != ErrPresenceClientClass {
			t.Errorf("class %q: got %v, want ErrPresenceClientClass", bad, err)
		}
	}
}

func TestPresencePayloadValidateInvalidClientVersion(t *testing.T) {
	p := validPayload()
	p.ClientVersion = ""
	if err := p.Validate(); err != ErrPresenceClientVersion {
		t.Errorf("empty version: got %v, want ErrPresenceClientVersion", err)
	}
	p.ClientVersion = strings.Repeat("v", 65)
	if err := p.Validate(); err != ErrPresenceClientVersion {
		t.Errorf("too long version: got %v, want ErrPresenceClientVersion", err)
	}
}

func TestPresencePayloadValidateInvalidEpochs(t *testing.T) {
	p := validPayload()
	p.Epoch1d = "2026/09/12"
	if err := p.Validate(); err != ErrPresenceEpoch1d {
		t.Errorf("bad epoch1d: got %v, want ErrPresenceEpoch1d", err)
	}

	p = validPayload()
	p.Epoch7d = "not-a-number"
	if err := p.Validate(); err != ErrPresenceEpoch7d {
		t.Errorf("bad epoch7d: got %v, want ErrPresenceEpoch7d", err)
	}

	p = validPayload()
	p.Epoch30d = "abc"
	if err := p.Validate(); err != ErrPresenceEpoch30d {
		t.Errorf("bad epoch30d: got %v, want ErrPresenceEpoch30d", err)
	}
}

func TestPresencePayloadValidateInvalidTokens(t *testing.T) {
	p := validPayload()
	p.Token1d = "tooshort"
	if err := p.Validate(); err != ErrPresenceToken1d {
		t.Errorf("short token1d: got %v, want ErrPresenceToken1d", err)
	}

	p = validPayload()
	p.Token7d = strings.Repeat("g", 32) // 'g' is not hex
	if err := p.Validate(); err != ErrPresenceToken7d {
		t.Errorf("non-hex token7d: got %v, want ErrPresenceToken7d", err)
	}

	p = validPayload()
	p.Token30d = strings.Repeat("z", 32)
	if err := p.Validate(); err != ErrPresenceToken30d {
		t.Errorf("non-hex token30d: got %v, want ErrPresenceToken30d", err)
	}
}

func TestIsPublicClientClass(t *testing.T) {
	for _, public := range []string{"ordinary", "ORDINARY", "external", "External"} {
		if !IsPublicClientClass(public) {
			t.Errorf("class %q should be public", public)
		}
	}
	for _, excluded := range []string{"farm", "ci", "verifier", "operator", "internal", "custom", "", "unknown"} {
		if IsPublicClientClass(excluded) {
			t.Errorf("class %q should NOT be public", excluded)
		}
	}
}
