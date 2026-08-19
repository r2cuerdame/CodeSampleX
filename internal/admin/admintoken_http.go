package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// adminTokenPrefix marks an operator API credential in logs and in a paste,
// so one that escapes into a file is recognisable for what it is.
const adminTokenPrefix = "csx_admin_"

// maxAdminTokenTTLDays bounds a bounded lifetime. Beyond this an operator is
// asking for a permanent credential and should say so.
const maxAdminTokenTTLDays = 3650

type issueAdminTokenRequest struct {
	Label     string `json:"label"`
	Count     int    `json:"count"`
	TTLDays   int    `json:"ttlDays"`
	Unlimited bool   `json:"unlimited"`
}

// lifetime resolves the requested expiry.
//
// A permanent credential must be asked for in so many words. An operator who
// simply omits the duration is not handed one: the strongest option in the
// system should never be what happens when a field is forgotten.
func (req issueAdminTokenRequest) lifetime(now time.Time) (time.Time, error) {
	switch {
	case req.Unlimited && req.TTLDays != 0:
		return time.Time{}, errors.New("choose either a lifetime or no expiry, not both")
	case req.Unlimited:
		return time.Time{}, nil
	case req.TTLDays > 0 && req.TTLDays <= maxAdminTokenTTLDays:
		return now.AddDate(0, 0, req.TTLDays), nil
	default:
		return time.Time{}, errors.New("a lifetime in days, or an explicit unlimited")
	}
}

func cleanAdminTokenLabel(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	if label == "" || len(label) > 80 || strings.ContainsAny(label, "\r\n\x00") {
		return "", errors.New("invalid admin token label")
	}
	return label, nil
}

func (h *handler) handleAdminTokens(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	byToken, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if h.adminTokens == nil {
		http.Error(w, "운영 토큰 저장소를 사용할 수 없습니다", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := h.adminTokens.ListAdminTokens(r.Context(), serverstore.MaxAdminTokens)
		if err != nil {
			http.Error(w, "운영 토큰을 불러오지 못했습니다", http.StatusServiceUnavailable)
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"tokens": adminTokenViews(rows)})
	case http.MethodPost:
		if !byToken && !h.validAdminMutation(r) {
			http.Error(w, "허용되지 않은 요청입니다", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, authoringRequestLimit)
		var input issueAdminTokenRequest
		if err := decodeAdminJSON(r, &input); err != nil {
			http.Error(w, "잘못된 요청입니다", http.StatusBadRequest)
			return
		}
		h.issueAdminTokens(w, r, input)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "허용되지 않은 요청 방식입니다", http.StatusMethodNotAllowed)
	}
}

func (h *handler) issueAdminTokens(w http.ResponseWriter, r *http.Request, input issueAdminTokenRequest) {
	label, err := cleanAdminTokenLabel(input.Label)
	if err != nil {
		http.Error(w, "이름이 올바르지 않습니다", http.StatusBadRequest)
		return
	}
	if input.Count < 1 || input.Count > 16 {
		http.Error(w, "발급 개수가 올바르지 않습니다", http.StatusBadRequest)
		return
	}
	now := h.now().UTC()
	expiresAt, err := input.lifetime(now)
	if err != nil {
		http.Error(w, "유효기간을 지정해 주세요", http.StatusBadRequest)
		return
	}

	raw := make([]byte, 44*input.Count)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		http.Error(w, "토큰을 만들지 못했습니다", http.StatusInternalServerError)
		return
	}
	rows := make([]serverstore.AdminTokenRow, 0, input.Count)
	out := make([]map[string]any, 0, input.Count)
	for i := 0; i < input.Count; i++ {
		chunk := raw[i*44 : (i+1)*44]
		secret := adminTokenPrefix + base64.RawURLEncoding.EncodeToString(chunk[:32])
		id := base64.RawURLEncoding.EncodeToString(chunk[32:])
		sum := sha256.Sum256([]byte(secret))
		// One label per machine: shared, a single misbehaving instance cannot
		// be cut off without cutting off the whole farm.
		name := label
		if input.Count > 1 {
			name = fmt.Sprintf("%s-%02d", label, i+1)
		}
		rows = append(rows, serverstore.AdminTokenRow{
			TokenHash: hex.EncodeToString(sum[:]), TokenID: id, Label: name,
			IssuedAt: now, ExpiresAt: expiresAt,
		})
		entry := map[string]any{"tokenId": id, "label": name, "token": secret}
		if !expiresAt.IsZero() {
			entry["expiresAt"] = expiresAt.Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	if err := h.adminTokens.IssueAdminTokens(r.Context(), rows); err != nil {
		http.Error(w, "토큰을 발급하지 못했습니다", http.StatusConflict)
		return
	}
	// The plaintext appears here and nowhere else, ever again.
	writeAdminJSON(w, http.StatusCreated, map[string]any{"tokens": out})
}

func (h *handler) revokeAdminToken(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	byToken, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if h.adminTokens == nil {
		http.Error(w, "운영 토큰 저장소를 사용할 수 없습니다", http.StatusServiceUnavailable)
		return
	}
	if !byToken && !h.validAdminMutation(r) {
		http.Error(w, "허용되지 않은 요청입니다", http.StatusForbidden)
		return
	}
	revoked, err := h.adminTokens.RevokeAdminToken(r.Context(), r.PathValue("id"), h.now().UTC())
	if err != nil {
		http.Error(w, "토큰을 폐기하지 못했습니다", http.StatusServiceUnavailable)
		return
	}
	if !revoked {
		http.Error(w, "토큰을 찾을 수 없습니다", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminTokenViews is the operator's ongoing view. It never carries a secret;
// lastUsedAt is what an unlimited credential offers instead of an expiry.
func adminTokenViews(rows []serverstore.AdminTokenRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		view := map[string]any{
			"tokenId":  row.TokenID,
			"label":    row.Label,
			"issuedAt": row.IssuedAt.UTC().Format(time.RFC3339),
			"revoked":  !row.RevokedAt.IsZero(),
		}
		if row.ExpiresAt.IsZero() {
			view["expiresAt"] = ""
		} else {
			view["expiresAt"] = row.ExpiresAt.UTC().Format(time.RFC3339)
		}
		if !row.LastUsedAt.IsZero() {
			view["lastUsedAt"] = row.LastUsedAt.UTC().Format(time.RFC3339)
			view["lastUsedIp"] = row.LastUsedIP
		}
		out = append(out, view)
	}
	return out
}
