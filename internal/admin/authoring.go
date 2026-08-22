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
	model, reasoning, err := cleanAuthoringModel(model, reasoning)
	if err != nil {
		return nil, err
	}
	// The operator used to type a machine name here. The session row already
	// carries ComputerName, which the worker reports about itself, so the
	// typed field duplicated it -- and could disagree with it, which is worse
	// than not having it. An omitted label falls back to the model, keeping
	// the batch suffix that makes a list of sessions readable.
	if strings.TrimSpace(label) == "" {
		label = model
	}
	label, err = cleanAuthoringLabel(label)
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

// authoringWorkerKey derives the directory name a worker's CSX_HOME lives
// under.
//
// It is DERIVED, never taken. cleanAuthoringLabel rejects only newlines and
// NUL because a label is free text shown in the console; as a path component
// inside a generated .bat and .sh it would carry "..\..\Windows" or
// `a" & del /q * & set "x=` straight into the script. So the key keeps only
// characters that are inert in both shells and in a path, and carries a short
// hash of the raw label so two labels that differ only in stripped characters
// cannot land on the same home — which would mean two workers sharing one
// identity.
//
// Keying on the WORKER rather than the session is the point: a session is
// disposable and a worker is not. A per-session home means a per-session
// identity, so a day of sessions reports as a day of distinct peers, and the
// peer-bucket count stops meaning anything. It also means csx init would have
// to run on every session instead of once per worker.
func authoringWorkerKey(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	slug := strings.Trim(b.String(), "-_")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	sum := sha256.Sum256([]byte(label))
	digest := hex.EncodeToString(sum[:])[:8]
	if slug == "" {
		return "w-" + digest
	}
	return slug + "-" + digest
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
- 서버가 배정한 일감 하나만 집중 공략한다. 일감은 사용자 Wanted, 반복 실패가 관측된 Finding 후보, 많이 쓰이지만 검증 샘플이 없는 커버리지 확장 중 하나다.
- 이 환경에서 실제로 준비·실행 가능한 일만 선택한다.
- 문서상 기대와 실측이 다르거나, 런타임/JDK/버전을 바꾸면 결과가 달라지는 Finding 후보를 우선한다.
- 커버리지 확장 일감에 심벌이 비어 있으면 해당 정확한 패키지·버전의 많이 쓰는 핵심 API 하나를 골라 구체적인 계약을 만든다. 배정되지 않은 다른 패키지로 바꾸지는 않는다.

필수 절차:
1. 기존 설정과 격리된 새 빈 CSX_HOME을 사용한다. 기존 config.json, apiToken, seeder/admin 자격은 읽거나 복사하지 않는다.
2. 아래 명령으로 가장 우선순위가 높은 일감 하나를 받는다. 서버는 미해결 Wanted를 먼저 주고, 없으면 실패 관측과 사용량으로 새 Finding·커버리지 일감을 만든다. NO_WORK면 임의 샘플을 만들지 말고 5분 기다린 뒤 다시 호출한다. 명시적으로 중지되거나 토큰 갱신이 실패할 때까지 이 재조회를 계속한다. 같은 세션에서 다시 호출하면 현재 임대가 그대로 나온다.
%s
2-1. 배정된 좌표에 호출할 수 있는 심벌·프로젝트가 실제로 없다고 판단했거나(예: jar 없는 pom, Gradle plugin marker, 부모가 내부에서 고르는 플랫폼별 .node 바이너리), 레지스트리·툴체인이 응답하지 않았거나, 이 기계 자체가 실패했다면 아래 명령으로 사유를 붙여 즉시 반납한다. 같은 좌표를 계속 다시 받아 시간을 태우지 말라 — 반납은 그 좌표에 대한 네트워크의 유일한 기록이고, 이 분류가 "일시적 장애"와 "작성 불가능"을 가르는 근거가 된다.
%s
   --outcome 값: no-callable-symbol(호출 가능한 심벌이 없다고 측정) | transient(레지스트리·툴체인 무응답) | infrastructure(내 기계·Docker 실패) | no-output(이유를 특정할 수 없음). --detail에는 운영자가 읽을 한 줄을 남긴다.
3. 배정된 공개 라이브러리 코드를 쓰기 전 CSX search_known_solution을 먼저 호출한다.
4. 빌드·테스트는 CSX run_observed_command로 실행한다.
5. MISS 후 해결하고 PASS했다면 propose_public_sample로 제안한다.
6. 출력된 csx sample propose 명령으로 시작한다. 이 명령이 spec.json, PROMPT.md와 올바른 csx.json 스캐폴드를 함께 만든다. spec.json을 csx.json으로 복사하거나 csx.json을 기억으로 새로 만들지 않는다.
7. 생성된 csx.json의 빈 contract와 환경·명령·verifierAdapter를 실제 파일에 맞게 완성한 뒤 csx sample create → csx sample verify → csx sample preview까지 수행하고 sample ID, 환경, PASS/FAIL, Finding을 보고한다.
8. preview까지 확인한 로컬 샘플은 아래 명령에서 <sampleId>를 실제 ID로 바꿔 비공개 초안함에 전송한 뒤 2번으로 돌아가 다음 일감을 확인한다.
%s
9. csx sample publish를 실행하지 않는다. 공개 HTTP 업로드나 yes 입력 우회도 금지한다. 지정 검증 워커가 계약 PASS 영수증을 제출하면 서버가 자동으로 CROSS_PASS 공개한다. 실패한 초안은 비공개 초안함에 남는다.

이 명령에 포함된 토큰은 작업 세션 갱신, Wanted 일감 임대와 비공개 초안 전송 외의 권한이 없다. 공개 게시·admin·검증 worker job·receipt 권한으로 사용하려 하지 말라.`, grant.Label, grant.Model, grant.Reasoning, refreshCommand, nextCommand,
		authoringReportCommand(baseURL, grant.Token), authoringSubmitCommand(baseURL, grant.Token))
}

func authoringCommand(baseURL, token string) string {
	return fmt.Sprintf("csx sample-worker refresh --server %q --token %q", strings.TrimRight(baseURL, "/"), token)
}

func authoringSubmitCommand(baseURL, token string) string {
	return fmt.Sprintf("csx sample-worker submit <sampleId> --server %q --token %q", strings.TrimRight(baseURL, "/"), token)
}

// authoringReportCommand is how a writer hands back work it cannot author,
// with the reason. Without it the only thing the server ever learns about a
// hopeless coordinate is silence, and silence is what a busy worker looks like.
func authoringReportCommand(baseURL, token string) string {
	return fmt.Sprintf("csx sample-worker report --outcome no-callable-symbol --detail %q --server %q --token %q",
		"왜 작성할 수 없는지 한 줄", strings.TrimRight(baseURL, "/"), token)
}

func authoringNextCommand(baseURL, token string) string {
	return fmt.Sprintf("csx sample-worker next --server %q --token %q", strings.TrimRight(baseURL, "/"), token)
}

func authoringWindowsCMD(baseURL string, grant authoringGrant) string {
	if !strings.EqualFold(strings.TrimSpace(grant.Model), "agy") {
		return ""
	}
	baseURL = strings.TrimRight(baseURL, "/")
	sessionID := safeAuthoringCMDID(grant.ID)
	prompt := authoringWindowsAgentPrompt(baseURL, grant)
	encoded := base64.StdEncoding.EncodeToString([]byte(prompt))
	const chunkSize = 3000
	lines := []string{
		"@echo off",
		"setlocal EnableExtensions DisableDelayedExpansion",
		"title CodeSampleX sample worker " + sessionID,
		`set "CSX_SESSION_ID=` + sessionID + `"`,
		`set "CSX_SERVER=` + baseURL + `"`,
		`set "CSX_TOKEN=` + grant.Token + `"`,
		`set "CSX_REASONING=` + grant.Reasoning + `"`,
		`set "CSX_WORKER=` + authoringWorkerKey(grant.Label) + `"`,
		`set "CSX_HOME=%LOCALAPPDATA%\CodeSampleX\sample-workers\%CSX_WORKER%"`,
		`set "CSX_REFRESH_LOG=%TEMP%\csx-worker-%CSX_SESSION_ID%-refresh.log"`,
		`set "CSX_NEXT_LOG=%TEMP%\csx-worker-%CSX_SESSION_ID%-next.log"`,
		`set "CSX_AGY_LOG=%TEMP%\csx-worker-%CSX_SESSION_ID%-agy.log"`,
		`set "CSX_AGY=%LOCALAPPDATA%\agy\bin\agy.exe"`,
		`if exist "%CSX_AGY%" goto :agent_ready`,
		`where agy.exe >nul 2>&1`,
		`if errorlevel 1 goto :missing_agent`,
		`set "CSX_AGY=agy.exe"`,
		`:agent_ready`,
		`if not exist "%CSX_HOME%" mkdir "%CSX_HOME%"`,
		// The home is keyed on the WORKER, so this runs once and later
		// sessions find it already done. Without it the home stays
		// uninitialized, and that mode is excluded from contacting registries
		// on purpose -- "before csx init, no mode has been chosen, so no
		// permission has been given" -- which silently made every
		// run_observed_command in step 4 record nothing at all.
		//
		// --no-agents and --no-daemon keep the isolation this home exists for:
		// a fresh identity and config here, and nothing written to the
		// operator's agent registrations or daemon.
		`if not exist "%CSX_HOME%\config.json" csx init --community --no-agents --no-daemon`,
	}
	refs := make([]string, 0, (len(encoded)+chunkSize-1)/chunkSize)
	for index, start := 0, 0; start < len(encoded); index, start = index+1, start+chunkSize {
		end := start + chunkSize
		if end > len(encoded) {
			end = len(encoded)
		}
		name := fmt.Sprintf("CSX_PROMPT_B64_%04d", index+1)
		lines = append(lines, fmt.Sprintf(`set "%s=%s"`, name, encoded[start:end]))
		refs = append(refs, "$env:"+name)
	}
	lines = append(lines,
		`echo CodeSampleX sample worker is running. Close this window or press Ctrl+C to stop.`,
		`:poll`,
		`csx sample-worker refresh --server "%CSX_SERVER%" --token "%CSX_TOKEN%" >"%CSX_REFRESH_LOG%" 2>&1`,
		`set "CSX_RC=%ERRORLEVEL%"`,
		`type "%CSX_REFRESH_LOG%"`,
		`if "%CSX_RC%"=="0" goto :next_work`,
		`findstr /c:"HTTP 410" "%CSX_REFRESH_LOG%" >nul 2>&1`,
		`if not errorlevel 1 goto :expired`,
		`echo Temporary refresh failure. Retrying in 60 seconds.`,
		`timeout /t 60 /nobreak >nul`,
		`goto :poll`,
		`:next_work`,
		`csx sample-worker next --server "%CSX_SERVER%" --token "%CSX_TOKEN%" >"%CSX_NEXT_LOG%" 2>&1`,
		`set "CSX_RC=%ERRORLEVEL%"`,
		`type "%CSX_NEXT_LOG%"`,
		`if not "%CSX_RC%"=="0" goto :retry_work`,
		`findstr /b /c:"NO_WORK:" "%CSX_NEXT_LOG%" >nul 2>&1`,
		`if not errorlevel 1 goto :idle`,
		`findstr /c:"No uncovered Wanted work" "%CSX_NEXT_LOG%" >nul 2>&1`,
		`if not errorlevel 1 goto :idle`,
		`powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -Command "$b64=`+strings.Join(refs, "+")+`; $prompt=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b64)); $agyArgs=@(); if($env:CSX_REASONING -in @('low','medium','high')){$agyArgs += @('--effort',$env:CSX_REASONING)}; $agyArgs += @('--dangerously-skip-permissions','--print-timeout','50m','--print',$prompt); Set-Content -LiteralPath $env:CSX_AGY_LOG -Value ('AGY iteration started ' + [DateTimeOffset]::Now.ToString('o')); & $env:CSX_AGY @agyArgs 2>&1 | Tee-Object -FilePath $env:CSX_AGY_LOG -Append; $rc=$LASTEXITCODE; Add-Content -LiteralPath $env:CSX_AGY_LOG -Value ('AGY iteration exited ' + $rc + ' ' + [DateTimeOffset]::Now.ToString('o')); exit $rc"`,
		`echo AGY iteration ended. Checking the same lease again in 10 seconds.`,
		`timeout /t 10 /nobreak >nul`,
		`goto :poll`,
		`:idle`,
		`echo No uncovered work is available yet. Checking again in 5 minutes.`,
		`timeout /t 300 /nobreak >nul`,
		`goto :poll`,
		`:retry_work`,
		`echo Work lookup failed. Retrying in 60 seconds.`,
		`timeout /t 60 /nobreak >nul`,
		`goto :poll`,
		`:expired`,
		`echo This sample-worker token expired. Rotate it in the CodeSampleX admin page and download a new CMD file.`,
		`pause`,
		`exit /b 2`,
		`:missing_agent`,
		`echo AGY was not found. Install AGY or add agy.exe to PATH, then run this CMD again.`,
		`pause`,
		`exit /b 3`,
	)
	return strings.Join(lines, "\r\n") + "\r\n"
}

func authoringLinuxSH(baseURL string, grant authoringGrant) string {
	if !strings.EqualFold(strings.TrimSpace(grant.Model), "agy") {
		return ""
	}
	baseURL = strings.TrimRight(baseURL, "/")
	sessionID := safeAuthoringCMDID(grant.ID)
	prompt := strings.ReplaceAll(authoringWindowsAgentPrompt(baseURL, grant), "CMD supervisor", "Linux shell supervisor")
	prompt = strings.ReplaceAll(prompt, "바깥 CMD supervisor", "바깥 Linux shell supervisor")
	encoded := base64.StdEncoding.EncodeToString([]byte(prompt))
	return strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -u",
		`export PATH="$HOME/.local/bin:$PATH"`,
		`CSX_SESSION_ID='` + sessionID + `'`,
		`CSX_SERVER='` + baseURL + `'`,
		`CSX_TOKEN='` + grant.Token + `'`,
		`CSX_REASONING='` + grant.Reasoning + `'`,
		`export CSX_WORKER="` + authoringWorkerKey(grant.Label) + `"`,
		`export CSX_HOME="$HOME/.local/share/CodeSampleX/sample-workers/$CSX_WORKER"`,
		`CSX_WORKSPACE="$CSX_HOME/workspace"`,
		`CSX_REFRESH_LOG="/tmp/csx-worker-$CSX_SESSION_ID-refresh.log"`,
		`CSX_NEXT_LOG="/tmp/csx-worker-$CSX_SESSION_ID-next.log"`,
		`CSX_AGY_LOG="/tmp/csx-worker-$CSX_SESSION_ID-agy.log"`,
		`CSX_PROMPT_B64='` + encoded + `'`,
		`mkdir -p "$CSX_HOME" "$CSX_WORKSPACE"`,
		// Once per worker, not once per session -- see the Windows script.
		`[ -f "$CSX_HOME/config.json" ] || csx init --community --no-agents --no-daemon`,
		`cd "$CSX_WORKSPACE"`,
		`command -v agy >/dev/null 2>&1 || { echo "AGY was not found. Install it with: curl -fsSL https://antigravity.google/cli/install.sh | bash"; exit 3; }`,
		`command -v csx >/dev/null 2>&1 || { echo "csx was not found. Install it with: curl -fsSL https://codesamplex.dev/install.sh | bash"; exit 3; }`,
		`printf '%s\n' 'CodeSampleX Linux sample worker is running. Press Ctrl+C to stop.'`,
		`while true; do`,
		`  if ! csx sample-worker refresh --server "$CSX_SERVER" --token "$CSX_TOKEN" >"$CSX_REFRESH_LOG" 2>&1; then`,
		`    cat "$CSX_REFRESH_LOG"`,
		`    if grep -q 'HTTP 410' "$CSX_REFRESH_LOG"; then echo 'This sample-worker token expired. Rotate it in the admin page and download a new SH file.'; exit 2; fi`,
		`    echo 'Temporary refresh failure. Retrying in 60 seconds.'; sleep 60; continue`,
		`  fi`,
		`  cat "$CSX_REFRESH_LOG"`,
		`  if ! csx sample-worker next --server "$CSX_SERVER" --token "$CSX_TOKEN" >"$CSX_NEXT_LOG" 2>&1; then`,
		`    cat "$CSX_NEXT_LOG"; echo 'Work lookup failed. Retrying in 60 seconds.'; sleep 60; continue`,
		`  fi`,
		`  cat "$CSX_NEXT_LOG"`,
		`  if grep -q -e '^NO_WORK:' -e 'No uncovered Wanted work' "$CSX_NEXT_LOG"; then echo 'No uncovered work is available yet. Checking again in 5 minutes.'; sleep 300; continue; fi`,
		`  prompt="$(printf '%s' "$CSX_PROMPT_B64" | base64 -d)"`,
		`  agy_args=(--dangerously-skip-permissions --print-timeout 50m)`,
		`  case "$CSX_REASONING" in low|medium|high) agy_args+=(--effort "$CSX_REASONING");; esac`,
		`  printf 'AGY iteration started %s\n' "$(date -Iseconds)" >"$CSX_AGY_LOG"`,
		`  agy "${agy_args[@]}" --print "$prompt" 2>&1 | tee -a "$CSX_AGY_LOG"`,
		`  rc=${PIPESTATUS[0]}`,
		`  printf 'AGY iteration exited %s %s\n' "$rc" "$(date -Iseconds)" >>"$CSX_AGY_LOG"`,
		`  echo 'AGY iteration ended. Checking the same lease again in 10 seconds.'`,
		`  sleep 10`,
		`done`,
	}, "\n") + "\n"
}

func authoringWindowsAgentPrompt(baseURL string, grant authoringGrant) string {
	return fmt.Sprintf(`CodeSampleX CMD supervisor가 배정한 샘플 일감 하나를 처리한다.

작업 식별: %s
지정 모델: agy
추론 강도: %s

규칙:
1. 현재 CSX_HOME만 사용하고 다른 프로필·토큰·자격·워크트리는 읽지 않는다.
2. supervisor가 이미 세션을 갱신하고 일감을 임대했다. 아래 명령을 다시 실행해 같은 현재 임대를 확인한다.
%s
3. 배정 종류는 사용자 Wanted, 반복 실패 기반 Finding, 사용량 기반 커버리지 확장 중 하나다. 커버리지 확장에서 심벌이 비어 있으면 정확히 배정된 패키지·버전의 기존 공개 샘플과 겹치지 않는 다음 핵심 API 하나를 골라 구체적인 계약을 만들며, 다른 패키지로 바꾸지 않는다.
4. 공개 라이브러리 코드를 쓰기 전 search_known_solution을 호출하고 빌드·테스트는 run_observed_command로 실행한다.
5. 진짜 MISS를 해결해 PASS한 경우에만 propose_public_sample을 호출한다. 출력된 sample propose 명령으로 시작하고, 생성된 csx.json 스캐폴드를 완성한다. spec.json을 csx.json으로 복사하거나 매니페스트를 기억으로 만들지 않는다.
6. csx sample create, verify, preview를 순서대로 통과시키고 leakage가 없는 로컬 샘플만 아래 명령의 <sampleId>를 실제 ID로 바꿔 비공개 제출한다.
%s
7. sample publish, 공개 HTTP 업로드, yes 입력 우회를 하지 않는다.
8. 한 샘플을 제출하거나 현재 임대를 처리할 수 없는 구체적 이유를 기록하면 이 AGY 실행을 끝낸다. 다음 일감과 재시작은 바깥 CMD supervisor가 담당한다.
9. 작업이 40분을 넘으면 아래 명령으로 세션을 갱신한다. 실패하면 새 작업을 시작하지 않고 종료한다.
%s`, grant.Label, grant.Reasoning, authoringNextCommand(baseURL, grant.Token), authoringSubmitCommand(baseURL, grant.Token), authoringCommand(baseURL, grant.Token))
}

func safeAuthoringCMDID(raw string) string {
	var b strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(raw)) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			b.WriteRune(char)
		}
	}
	if b.Len() == 0 {
		return "worker"
	}
	return b.String()
}
