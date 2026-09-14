package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func validTestPresencePayload() domain.PresencePayload {
	return domain.PresencePayload{
		SchemaVersion: 1,
		ClientClass:   "ordinary",
		ClientVersion: "dev",
		Epoch1d:       "2026-09-12",
		Token1d:       strings.Repeat("a", 32),
		Epoch7d:       "2958",
		Token7d:       strings.Repeat("b", 32),
		Epoch30d:      "690",
		Token30d:      strings.Repeat("c", 32),
	}
}

func TestPresenceEndpointValidation(t *testing.T) {
	fake := serverstore.NewFake()
	mux := NewMux(Deps{Store: fake})

	tests := []struct {
		name       string
		modify     func(p *domain.PresencePayload)
		rawBody    string
		wantStatus int
	}{
		{
			name:       "valid payload",
			wantStatus: http.StatusOK,
		},
		{
			name: "invalid schema version",
			modify: func(p *domain.PresencePayload) {
				p.SchemaVersion = 2
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid client class with spaces",
			modify: func(p *domain.PresencePayload) {
				p.ClientClass = "bad class"
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "empty client version",
			modify: func(p *domain.PresencePayload) {
				p.ClientVersion = ""
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid epoch1d format",
			modify: func(p *domain.PresencePayload) {
				p.Epoch1d = "2026/09/12"
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid epoch7d non-numeric",
			modify: func(p *domain.PresencePayload) {
				p.Epoch7d = "not-a-number"
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid epoch30d non-numeric",
			modify: func(p *domain.PresencePayload) {
				p.Epoch30d = "abc"
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "token1d too short",
			modify: func(p *domain.PresencePayload) {
				p.Token1d = "1234"
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "token7d non-hex",
			modify: func(p *domain.PresencePayload) {
				p.Token7d = strings.Repeat("z", 32)
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON",
			rawBody:    "{not json",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "body too large",
			rawBody:    `{"token1d":"` + strings.Repeat("a", 5000) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			if tc.rawBody != "" {
				body = []byte(tc.rawBody)
			} else {
				p := validTestPresencePayload()
				if tc.modify != nil {
					tc.modify(&p)
				}
				var err error
				body, err = json.Marshal(p)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
			}

			req := httptest.NewRequest(http.MethodPost, "/v1/presence", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantStatus == http.StatusOK {
				var resp map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if resp["status"] != "accepted" {
					t.Errorf("status field = %q, want accepted", resp["status"])
				}
			}
		})
	}
}

func TestPresenceEndpointIdempotencyAndNoIPOrUAPersistence(t *testing.T) {
	fake := serverstore.NewFake()
	mux := NewMux(Deps{Store: fake})

	p := validTestPresencePayload()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	// First request from IP 1.2.3.4 and UA "Go-Agent/1"
	req1 := httptest.NewRequest(http.MethodPost, "/v1/presence", bytes.NewReader(body))
	req1.RemoteAddr = "1.2.3.4:12345"
	req1.Header.Set("User-Agent", "Go-Agent/1")
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("req1 status = %d, want 200", rec1.Code)
	}

	// Repeated identical request from completely different IP and UA
	req2 := httptest.NewRequest(http.MethodPost, "/v1/presence", bytes.NewReader(body))
	req2.RemoteAddr = "9.8.7.6:54321"
	req2.Header.Set("User-Agent", "Different-Client/2")
	req2.Header.Set("X-Forwarded-For", "5.5.5.5")
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("req2 status = %d, want 200", rec2.Code)
	}

	// Check store counts: exactly 1 active installation counted for 1d, 7d, 30d
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	counts, err := fake.NetworkCounts(context.Background(), now)
	if err != nil {
		t.Fatalf("NetworkCounts: %v", err)
	}
	if counts.ActiveInstallations1d != 1 {
		t.Errorf("ActiveInstallations1d = %d, want 1", counts.ActiveInstallations1d)
	}
	if counts.ActiveInstallations7d != 1 {
		t.Errorf("ActiveInstallations7d = %d, want 1", counts.ActiveInstallations7d)
	}
	if counts.ActiveInstallations30d != 1 {
		t.Errorf("ActiveInstallations30d = %d, want 1", counts.ActiveInstallations30d)
	}
}

func TestPresenceEndpointClientClassExclusion(t *testing.T) {
	fake := serverstore.NewFake()
	mux := NewMux(Deps{Store: fake})

	// Post an internal/ci presence report
	p := validTestPresencePayload()
	p.ClientClass = "ci"
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/presence", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// Internal client class is stored, but excluded from public counts
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	counts, err := fake.NetworkCounts(context.Background(), now)
	if err != nil {
		t.Fatalf("NetworkCounts: %v", err)
	}
	if counts.ActiveInstallations1d != 0 {
		t.Errorf("ActiveInstallations1d = %d, want 0 for internal class 'ci'", counts.ActiveInstallations1d)
	}
	if counts.ActiveInstallations7d != 0 {
		t.Errorf("ActiveInstallations7d = %d, want 0 for internal class 'ci'", counts.ActiveInstallations7d)
	}
	if counts.ActiveInstallations30d != 0 {
		t.Errorf("ActiveInstallations30d = %d, want 0 for internal class 'ci'", counts.ActiveInstallations30d)
	}
}
