package admin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/activity"
)

const authoringRequestLimit = 2048

const (
	authoringIPRefreshLimit    = 60
	authoringTokenRefreshLimit = 12
	authoringRateWindow        = time.Minute
	maxAuthoringRateKeys       = 2048
)

type authoringRateEntry struct {
	start time.Time
	count int
}

type authoringRateLimiter struct {
	mu      sync.Mutex
	entries map[string]authoringRateEntry
}

func newAuthoringRateLimiter() *authoringRateLimiter {
	return &authoringRateLimiter{entries: make(map[string]authoringRateEntry)}
}

func (l *authoringRateLimiter) allow(key string, now time.Time, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry, ok := l.entries[key]; ok {
		if now.Sub(entry.start) < authoringRateWindow {
			if entry.count >= limit {
				return false
			}
			entry.count++
			l.entries[key] = entry
			return true
		}
		delete(l.entries, key)
	}
	if len(l.entries) >= maxAuthoringRateKeys {
		for candidate, entry := range l.entries {
			if now.Sub(entry.start) >= authoringRateWindow {
				delete(l.entries, candidate)
			}
		}
		if len(l.entries) >= maxAuthoringRateKeys {
			return false
		}
	}
	l.entries[key] = authoringRateEntry{start: now, count: 1}
	return true
}

//go:embed static/admin.js
var adminStaticFS embed.FS

type issueAuthoringRequest struct {
	Label     string `json:"label"`
	Model     string `json:"model"`
	Reasoning string `json:"reasoning"`
	Count     int    `json:"count"`
}

type issueAuthoringResponse struct {
	Prompt  string                  `json:"prompt"`
	Workers []issuedAuthoringWorker `json:"workers"`
}

type issuedAuthoringWorker struct {
	Command string         `json:"command"`
	Session authoringGrant `json:"session"`
}

func (h *handler) authoringSessions(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if !h.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		sessions, err := h.authoring.ListContext(r.Context())
		if err != nil {
			http.Error(w, "샘플 워커 세션을 불러오지 못했습니다", http.StatusServiceUnavailable)
			return
		}
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].IssuedAt.After(sessions[j].IssuedAt) })
		writeAdminJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
	case http.MethodPost:
		if !h.validAdminMutation(r) {
			http.Error(w, "허용되지 않은 요청입니다", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, authoringRequestLimit)
		var input issueAuthoringRequest
		if err := decodeAdminJSON(r, &input); err != nil {
			http.Error(w, "작업 식별을 확인하세요", http.StatusBadRequest)
			return
		}
		grants, err := h.authoring.IssueBatchContext(r.Context(), input.Label, input.Model, input.Reasoning, input.Count)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errAuthoringFull) {
				status = http.StatusConflict
			}
			http.Error(w, "샘플 워커 세션을 발급하지 못했습니다", status)
			return
		}
		workers := make([]issuedAuthoringWorker, 0, len(grants))
		prompts := make([]string, 0, len(grants))
		for index, grant := range grants {
			workers = append(workers, issuedAuthoringWorker{Command: authoringCommand(h.publicURL, grant.Token), Session: grant})
			prompts = append(prompts, fmt.Sprintf("===== SAMPLE WORKER %d/%d · %s =====\n%s", index+1, len(grants), grant.Label, authoringPrompt(h.publicURL, grant)))
		}
		writeAdminJSON(w, http.StatusCreated, issueAuthoringResponse{Prompt: strings.Join(prompts, "\n\n"), Workers: workers})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "허용되지 않은 요청 방식입니다", http.StatusMethodNotAllowed)
	}
}

func (h *handler) authoringDrafts(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if !h.requireAdmin(w, r) {
		return
	}
	if h.authoring.store == nil {
		writeAdminJSON(w, http.StatusOK, map[string]any{"drafts": []any{}})
		return
	}
	rows, err := h.authoring.store.ListAuthoringDrafts(r.Context(), 100)
	if err != nil {
		http.Error(w, "샘플 초안함을 불러오지 못했습니다", http.StatusServiceUnavailable)
		return
	}
	type draftView struct {
		SampleID           string    `json:"sampleId"`
		WorkerLabel        string    `json:"workerLabel"`
		LocalStatus        string    `json:"localStatus"`
		VerificationStatus string    `json:"verificationStatus"`
		Goal               string    `json:"goal"`
		Packages           []string  `json:"packages"`
		Symbols            []string  `json:"symbols"`
		UpdatedAt          time.Time `json:"updatedAt"`
	}
	out := make([]draftView, 0, len(rows))
	for _, row := range rows {
		var manifest struct {
			Packages []string `json:"packages"`
			Symbols  []string `json:"symbols"`
			Case     struct {
				Goal string `json:"goal"`
			} `json:"case"`
		}
		if err := json.Unmarshal([]byte(row.ManifestJSON), &manifest); err != nil {
			continue
		}
		out = append(out, draftView{SampleID: row.SampleID, WorkerLabel: row.WorkerLabel,
			LocalStatus: row.LocalStatus, VerificationStatus: row.VerificationStatus, Goal: manifest.Case.Goal, Packages: manifest.Packages,
			Symbols: manifest.Symbols, UpdatedAt: row.UpdatedAt})
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"drafts": out})
}

func (h *handler) revokeAuthoringSession(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if !h.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "허용되지 않은 요청 방식입니다", http.StatusMethodNotAllowed)
		return
	}
	if !h.validAdminMutation(r) {
		http.Error(w, "허용되지 않은 요청입니다", http.StatusForbidden)
		return
	}
	if err := h.authoring.RevokeIDContext(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, "세션을 찾을 수 없습니다", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) rotateAuthoringSession(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if !h.requireAdmin(w, r) {
		return
	}
	if !h.validAdminMutation(r) {
		http.Error(w, "허용되지 않은 요청입니다", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, authoringRequestLimit)
	var payload struct{}
	if err := decodeAdminJSON(r, &payload); err != nil {
		http.Error(w, "잘못된 요청입니다", http.StatusBadRequest)
		return
	}
	grant, err := h.authoring.RotateIDContext(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "세션을 찾을 수 없습니다", http.StatusNotFound)
		return
	}
	worker := issuedAuthoringWorker{Command: authoringCommand(h.publicURL, grant.Token), Session: grant}
	writeAdminJSON(w, http.StatusOK, map[string]any{"prompt": authoringPrompt(h.publicURL, grant), "worker": worker})
}

func (h *handler) refreshAuthoringSession(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, authoringRequestLimit)
	var payload struct {
		ComputerName string `json:"computerName"`
	}
	if err := decodeAdminJSON(r, &payload); err != nil {
		http.Error(w, "invalid refresh request", http.StatusBadRequest)
		return
	}
	now := h.now().UTC()
	ip := externalAddress(r)
	if ip == "" {
		ip = "unknown"
	}
	if !h.authoringRate.allow("ip:"+ip, now, authoringIPRefreshLimit) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "refresh rate exceeded", http.StatusTooManyRequests)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "session unavailable", http.StatusUnauthorized)
		return
	}
	hash := sha256.Sum256([]byte(token))
	if !h.authoringRate.allow("token:"+hex.EncodeToString(hash[:]), now, authoringTokenRefreshLimit) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "refresh rate exceeded", http.StatusTooManyRequests)
		return
	}
	if ip == "unknown" {
		ip = ""
	}
	computerName := strings.TrimSpace(payload.ComputerName)
	if len(computerName) > 120 || strings.ContainsAny(computerName, "\r\n\x00") {
		http.Error(w, "invalid computer name", http.StatusBadRequest)
		return
	}
	grant, err := h.authoring.RefreshContext(r.Context(), token, ip, computerName)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, errAuthoringExpired) {
			status = http.StatusGone
		}
		http.Error(w, "session unavailable", status)
		return
	}
	writeAdminJSON(w, http.StatusOK, grant)
}

func (h *handler) adminScript(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}
	body, err := adminStaticFS.ReadFile("static/admin.js")
	if err != nil {
		http.Error(w, "script unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(".js"))
	w.Header().Set("Content-Length", stringInt(len(body)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

func (h *handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.authorized(r) {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="CodeSampleX Admin", charset="UTF-8"`)
	http.Error(w, "인증이 필요합니다", http.StatusUnauthorized)
	return false
}

func (h *handler) validAdminMutation(r *http.Request) bool {
	return h.publicURL != "" && r.Header.Get("Origin") == h.publicURL && r.Header.Get("X-CSX-CSRF") == "1" &&
		isJSONContentType(r.Header.Get("Content-Type"))
}

func isJSONContentType(raw string) bool {
	mediaType, _, err := mime.ParseMediaType(raw)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func decodeAdminJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, authoringRequestLimit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	returnValue := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		returnValue = parts[1]
	}
	if _, ok := validAuthoringToken(returnValue); !ok {
		return "", false
	}
	return returnValue, true
}

func externalAddress(r *http.Request) string {
	if addr, ok := activity.ExternalRequestAddress(r); ok {
		return addr.String()
	}
	return ""
}

func writeAdminJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "response unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", stringInt(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func stringInt(value int) string {
	return fmt.Sprintf("%d", value)
}
