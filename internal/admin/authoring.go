package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const (
	authoringTokenPrefix  = "csx_author_v1_"
	authoringIdleLifetime = time.Hour
	maxAuthoringSessions  = 64
)

var (
	errAuthoringInvalid = errors.New("authoring session is invalid")
	errAuthoringExpired = errors.New("authoring session expired")
	errAuthoringFull    = errors.New("too many active authoring sessions")
)

// authoringRegistry issues narrowly scoped refresh bearers. Production stores
// only their hashes in PostgreSQL so operator-started loops survive deploys;
// unit tests can use the bounded in-memory fallback. The bearer is accepted
// only by refresh and private-draft submission; public publish, identity,
// admin, verification-job and receipt endpoints reject it.
type authoringRegistry struct {
	mu      sync.Mutex
	now     func() time.Time
	random  io.Reader
	session map[[sha256.Size]byte]authoringSession
	store   serverstore.AuthoringSessionStore
}

type authoringSession struct {
	id            string
	label         string
	model         string
	reasoning     string
	issuedAt      time.Time
	lastRefreshAt time.Time
	idleExpiresAt time.Time
	lastIP        string
	computerName  string
}

type authoringGrant struct {
	ID            string    `json:"sessionId"`
	Token         string    `json:"-"`
	Label         string    `json:"label"`
	Model         string    `json:"model"`
	Reasoning     string    `json:"reasoning"`
	IdleExpiresAt time.Time `json:"idleExpiresAt"`
}

type authoringSessionView struct {
	ID            string     `json:"sessionId"`
	Label         string     `json:"label"`
	Model         string     `json:"model"`
	Reasoning     string     `json:"reasoning"`
	IssuedAt      time.Time  `json:"issuedAt"`
	LastRefreshAt *time.Time `json:"lastRefreshedAt,omitempty"`
	IdleExpiresAt time.Time  `json:"idleExpiresAt"`
	LastIP        string     `json:"lastIp,omitempty"`
	ComputerName  string     `json:"computerName,omitempty"`
}

func newAuthoringRegistry(now func() time.Time, stores ...serverstore.AuthoringSessionStore) *authoringRegistry {
	if now == nil {
		now = time.Now
	}
	r := &authoringRegistry{now: now, random: rand.Reader, session: make(map[[sha256.Size]byte]authoringSession)}
	if len(stores) > 0 {
		r.store = stores[0]
	}
	return r
}

// Issue creates one independently revocable authoring loop. Expired sessions
// are discarded first and the bounded cap prevents an authenticated browser
// accident from growing memory without limit.
func (r *authoringRegistry) Issue(label, model, reasoning string) (authoringGrant, error) {
	grants, err := r.IssueBatch(label, model, reasoning, 1)
	if err != nil {
		return authoringGrant{}, err
	}
	return grants[0], nil
}

func (r *authoringRegistry) IssueBatch(label, model, reasoning string, count int) ([]authoringGrant, error) {
	return r.IssueBatchContext(context.Background(), label, model, reasoning, count)
}

func (r *authoringRegistry) IssueBatchContext(ctx context.Context, label, model, reasoning string, count int) ([]authoringGrant, error) {
	label, err := cleanAuthoringLabel(label)
	if err != nil {
		return nil, err
	}
	model, reasoning, err = cleanAuthoringModel(model, reasoning)
	if err != nil {
		return nil, err
	}
	if count < 1 || count > 16 {
		return nil, errors.New("invalid authoring worker count")
	}
	raw := make([]byte, 44*count)
	if _, err := io.ReadFull(r.random, raw); err != nil {
		return nil, err
	}
	now := r.now().UTC()
	grants := make([]authoringGrant, 0, count)
	sessions := make([]authoringSession, 0, count)
	rows := make([]serverstore.AuthoringSessionRow, 0, count)
	for i := 0; i < count; i++ {
		workerLabel := label
		if count > 1 {
			workerLabel = fmt.Sprintf("%s-%02d", label, i+1)
			if len(workerLabel) > 80 {
				return nil, errors.New("authoring session label is too long for worker suffix")
			}
		}
		chunk := raw[i*44 : (i+1)*44]
		token := authoringTokenPrefix + base64.RawURLEncoding.EncodeToString(chunk[:32])
		id := base64.RawURLEncoding.EncodeToString(chunk[32:])
		hash := sha256.Sum256([]byte(token))
		grant := authoringGrant{
			ID: id, Token: token, Label: workerLabel, Model: model, Reasoning: reasoning, IdleExpiresAt: now.Add(authoringIdleLifetime),
		}
		session := authoringSession{
			id: id, label: workerLabel, model: model, reasoning: reasoning, issuedAt: now,
			idleExpiresAt: grant.IdleExpiresAt,
		}
		sessions = append(sessions, session)
		rows = append(rows, serverstore.AuthoringSessionRow{
			TokenHash: hex.EncodeToString(hash[:]), SessionID: id, Label: workerLabel,
			Model: model, Reasoning: reasoning, IssuedAt: now, IdleExpiresAt: grant.IdleExpiresAt,
		})
		grants = append(grants, grant)
	}
	if r.store != nil {
		if err := r.store.IssueAuthoringSessions(ctx, rows, now); err != nil {
			if errors.Is(err, serverstore.ErrAuthoringSessionLimit) {
				return nil, errAuthoringFull
			}
			return nil, err
		}
		return grants, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purgeExpiredLocked(now)
	if len(r.session)+count > maxAuthoringSessions {
		return nil, errAuthoringFull
	}
	for i, grant := range grants {
		hash, _ := validAuthoringToken(grant.Token)
		r.session[hash] = sessions[i]
	}
	return grants, nil
}

func (r *authoringRegistry) Refresh(token, ip string) (authoringGrant, error) {
	return r.RefreshContext(context.Background(), token, ip, "")
}

func (r *authoringRegistry) RotateIDContext(ctx context.Context, id string) (authoringGrant, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(r.random, raw); err != nil {
		return authoringGrant{}, err
	}
	token := authoringTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	now := r.now().UTC()
	if r.store != nil {
		row, err := r.store.RotateAuthoringSession(ctx, id, hex.EncodeToString(hash[:]), now, now.Add(authoringIdleLifetime))
		if err != nil {
			if errors.Is(err, serverstore.ErrAuthoringSessionMissing) {
				return authoringGrant{}, errAuthoringInvalid
			}
			return authoringGrant{}, err
		}
		grant := grantFromStored(row)
		grant.Token = token
		return grant, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for oldHash, session := range r.session {
		if session.id != id || !now.Before(session.idleExpiresAt) {
			continue
		}
		delete(r.session, oldHash)
		session.lastRefreshAt = time.Time{}
		session.idleExpiresAt = now.Add(authoringIdleLifetime)
		session.lastIP = ""
		session.computerName = ""
		r.session[hash] = session
		return authoringGrant{ID: id, Token: token, Label: session.label, Model: session.model, Reasoning: session.reasoning, IdleExpiresAt: session.idleExpiresAt}, nil
	}
	return authoringGrant{}, errAuthoringInvalid
}

func (r *authoringRegistry) RefreshContext(ctx context.Context, token, ip, computerName string) (authoringGrant, error) {
	hash, ok := validAuthoringToken(token)
	if !ok {
		return authoringGrant{}, errAuthoringInvalid
	}
	now := r.now().UTC()
	if r.store != nil {
		row, err := r.store.RefreshAuthoringSession(ctx, hex.EncodeToString(hash[:]), ip, computerName, now, now.Add(authoringIdleLifetime))
		if err != nil {
			switch {
			case errors.Is(err, serverstore.ErrAuthoringSessionExpired):
				return authoringGrant{}, errAuthoringExpired
			case errors.Is(err, serverstore.ErrAuthoringSessionMissing):
				return authoringGrant{}, errAuthoringInvalid
			default:
				return authoringGrant{}, err
			}
		}
		return grantFromStored(row), nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.session[hash]
	if !ok {
		return authoringGrant{}, errAuthoringInvalid
	}
	if !now.Before(session.idleExpiresAt) {
		delete(r.session, hash)
		return authoringGrant{}, errAuthoringExpired
	}
	session.lastRefreshAt = now
	session.idleExpiresAt = now.Add(authoringIdleLifetime)
	if ip != "" {
		session.lastIP = ip
	}
	if computerName != "" {
		session.computerName = computerName
	}
	r.session[hash] = session
	return authoringGrant{ID: session.id, Label: session.label, Model: session.model, Reasoning: session.reasoning, IdleExpiresAt: session.idleExpiresAt}, nil
}

func (r *authoringRegistry) RevokeID(id string) error {
	return r.RevokeIDContext(context.Background(), id)
}

func (r *authoringRegistry) RevokeIDContext(ctx context.Context, id string) error {
	if r.store != nil {
		ok, err := r.store.RevokeAuthoringSession(ctx, id, r.now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return errAuthoringInvalid
		}
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for hash, session := range r.session {
		if session.id == id {
			delete(r.session, hash)
			return nil
		}
	}
	return errAuthoringInvalid
}

func (r *authoringRegistry) List() []authoringSessionView {
	views, _ := r.ListContext(context.Background())
	return views
}

func (r *authoringRegistry) ListContext(ctx context.Context) ([]authoringSessionView, error) {
	if r.store != nil {
		rows, err := r.store.ListAuthoringSessions(ctx, r.now().UTC(), maxAuthoringSessions)
		if err != nil {
			return nil, err
		}
		out := make([]authoringSessionView, 0, len(rows))
		for _, row := range rows {
			out = append(out, viewFromStored(row))
		}
		return out, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.purgeExpiredLocked(now)
	out := make([]authoringSessionView, 0, len(r.session))
	for _, session := range r.session {
		var lastRefresh *time.Time
		if !session.lastRefreshAt.IsZero() {
			value := session.lastRefreshAt
			lastRefresh = &value
		}
		out = append(out, authoringSessionView{
			ID: session.id, Label: session.label, Model: session.model, Reasoning: session.reasoning, IssuedAt: session.issuedAt,
			LastRefreshAt: lastRefresh, IdleExpiresAt: session.idleExpiresAt, LastIP: session.lastIP, ComputerName: session.computerName,
		})
	}
	return out, nil
}

func grantFromStored(row serverstore.AuthoringSessionRow) authoringGrant {
	return authoringGrant{ID: row.SessionID, Label: row.Label, Model: row.Model, Reasoning: row.Reasoning, IdleExpiresAt: row.IdleExpiresAt}
}

func viewFromStored(row serverstore.AuthoringSessionRow) authoringSessionView {
	var lastRefresh *time.Time
	if !row.LastRefreshAt.IsZero() {
		value := row.LastRefreshAt
		lastRefresh = &value
	}
	return authoringSessionView{
		ID: row.SessionID, Label: row.Label, Model: row.Model, Reasoning: row.Reasoning,
		IssuedAt: row.IssuedAt, LastRefreshAt: lastRefresh, IdleExpiresAt: row.IdleExpiresAt,
		LastIP:       row.LastRefreshIP,
		ComputerName: row.ComputerName,
	}
}

func (r *authoringRegistry) purgeExpiredLocked(now time.Time) {
	for hash, session := range r.session {
		if !now.Before(session.idleExpiresAt) {
			delete(r.session, hash)
		}
	}
}

func cleanAuthoringLabel(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	if label == "" || len(label) > 80 || strings.ContainsAny(label, "\r\n\x00") {
		return "", errors.New("invalid authoring session label")
	}
	return label, nil
}

func cleanAuthoringModel(rawModel, rawReasoning string) (string, string, error) {
	model := strings.TrimSpace(rawModel)
	if model == "" || len(model) > 80 || strings.ContainsAny(model, "\r\n\x00") {
		return "", "", errors.New("invalid authoring model")
	}
	reasoning := strings.ToLower(strings.TrimSpace(rawReasoning))
	switch reasoning {
	case "auto", "low", "medium", "high":
	default:
		return "", "", errors.New("invalid authoring reasoning")
	}
	return model, reasoning, nil
}

func validAuthoringToken(token string) ([sha256.Size]byte, bool) {
	var zero [sha256.Size]byte
	if !strings.HasPrefix(token, authoringTokenPrefix) {
		return zero, false
	}
	encoded := strings.TrimPrefix(token, authoringTokenPrefix)
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 || encoded != base64.RawURLEncoding.EncodeToString(raw) {
		return zero, false
	}
	return sha256.Sum256([]byte(token)), true
}

func authoringPrompt(baseURL string, grant authoringGrant) string {
	baseURL = strings.TrimRight(baseURL, "/")
	refreshCommand := authoringCommand(baseURL, grant.Token)
	nextCommand := authoringNextCommand(baseURL, grant.Token)
	return fmt.Sprintf(`CodeSampleX 샘플 작성 세션을 시작한다.

작업 식별: %s
지정 모델: %s
추론 강도: %s (작업 난이도에 맞게 조정하되, 고비용 모델이 필요 없는 단순 작업은 가벼운 모델을 유지한다.)
유효 경계: 총수명 제한은 없지만 1시간 동안 갱신이 없으면 만료한다. 45분마다 아래 명령을 그대로 실행하고 실패하면 새 작업을 시작하지 말고 즉시 멈춘다.
%s

목표:
- 서버가 배정한 미해결 Wanted 패키지·버전·심벌 하나만 집중 공략한다.
- 이 환경에서 실제로 준비·실행 가능한 일만 선택한다.
- 문서상 기대와 실측이 다르거나, 런타임/JDK/버전을 바꾸면 결과가 달라지는 Finding 후보를 우선한다.

필수 절차:
1. 기존 설정과 격리된 새 빈 CSX_HOME을 사용한다. 기존 config.json, apiToken, seeder/admin 자격은 읽거나 복사하지 않는다.
2. 아래 명령으로 Wanted 일감 하나를 받는다. NO_WORK면 임의 샘플을 만들지 말고 멈춘다. 같은 세션에서 다시 호출하면 현재 임대가 그대로 나온다.
%s
3. 배정된 공개 라이브러리 코드를 쓰기 전 CSX search_known_solution을 먼저 호출한다.
4. 빌드·테스트는 CSX run_observed_command로 실행한다.
5. MISS 후 해결하고 PASS했다면 propose_public_sample로 제안한다.
6. 출력된 csx sample propose 명령으로 시작한다. 이 명령이 spec.json, PROMPT.md와 올바른 csx.json 스캐폴드를 함께 만든다. spec.json을 csx.json으로 복사하거나 csx.json을 기억으로 새로 만들지 않는다.
7. 생성된 csx.json의 빈 contract와 환경·명령·verifierAdapter를 실제 파일에 맞게 완성한 뒤 csx sample create → csx sample verify → csx sample preview까지 수행하고 sample ID, 환경, PASS/FAIL, Finding을 보고한다.
8. preview까지 확인한 로컬 샘플은 아래 명령에서 <sampleId>를 실제 ID로 바꿔 비공개 초안함에 전송한다.
%s
9. csx sample publish를 실행하지 않는다. 공개 HTTP 업로드나 yes 입력 우회도 금지한다. 지정 검증 워커가 계약 PASS 영수증을 제출하면 서버가 자동으로 CROSS_PASS 공개한다. 실패한 초안은 비공개 초안함에 남는다.

이 명령에 포함된 토큰은 작업 세션 갱신, Wanted 일감 임대와 비공개 초안 전송 외의 권한이 없다. 공개 게시·admin·검증 worker job·receipt 권한으로 사용하려 하지 말라.`, grant.Label, grant.Model, grant.Reasoning, refreshCommand, nextCommand, authoringSubmitCommand(baseURL, grant.Token))
}

func authoringCommand(baseURL, token string) string {
	return fmt.Sprintf("csx sample-worker refresh --server %q --token %q", strings.TrimRight(baseURL, "/"), token)
}

func authoringSubmitCommand(baseURL, token string) string {
	return fmt.Sprintf("csx sample-worker submit <sampleId> --server %q --token %q", strings.TrimRight(baseURL, "/"), token)
}

func authoringNextCommand(baseURL, token string) string {
	return fmt.Sprintf("csx sample-worker next --server %q --token %q", strings.TrimRight(baseURL, "/"), token)
}
