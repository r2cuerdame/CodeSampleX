package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The public copy and the grid it describes had drifted apart in every
// direction at once.
//
// The nine READMEs still drew a check mark that buildPivotCell stopped
// rendering, still promised a 90-day evidence half-life that
// internal/compatibility/confidence.go deliberately removed, and still called
// an observation count "recorded runs" from "real machines" — while the
// tooltip the same function writes says "observations" precisely because one
// build files one per stage it reached. None of it was caught, because
// nothing tied the sentence to the function it describes.
//
// These tests are that tie. They read the checked-in public documents and
// compare them against what the code actually produces, so a change to the
// implementation that leaves the copy behind fails here rather than in
// public.

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// publicREADMEs is every README a reader can land on: the English original
// and the eight translations its language bar links to. A fix that lands in
// one of them and not the other eight is the bug this list exists to catch.
func publicREADMEs() []string {
	return []string{
		"README.md",
		"docs/i18n/README.ko.md",
		"docs/i18n/README.ja.md",
		"docs/i18n/README.zh-CN.md",
		"docs/i18n/README.es.md",
		"docs/i18n/README.fr.md",
		"docs/i18n/README.de.md",
		"docs/i18n/README.pt-BR.md",
		"docs/i18n/README.ru.md",
	}
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

// gridMarks asks buildPivotCell itself what it puts in a cell rather than
// restating the answer. Everything below compares the documents to these
// three strings, so moving the glyph in one place moves the whole contract.
func gridMarks(t *testing.T) (ourRunPassed, ourRunFailed, nothingRecorded string) {
	t.Helper()
	now := time.Now()
	ourRunPassed = buildPivotCell(&pivotAgg{verPass: 1}, now).Glyph
	ourRunFailed = buildPivotCell(&pivotAgg{verFail: 1}, now).Glyph
	nothingRecorded = buildPivotCell(nil, now).Glyph
	if ourRunPassed == "" || ourRunFailed == "" || nothingRecorded == "" {
		t.Fatalf("buildPivotCell returned no glyph for a cell that has one: %q/%q/%q",
			ourRunPassed, ourRunFailed, nothingRecorded)
	}
	return ourRunPassed, ourRunFailed, nothingRecorded
}

// fencedBlockContaining returns the fenced code block that holds needle. The
// README example grids are the one place a reader sees the glyph before
// anything explains it, so they are checked as a unit rather than by scanning
// the whole file — a check mark is legitimate prose elsewhere, where nothing
// claims it is a cell.
func fencedBlockContaining(doc, needle string) (string, bool) {
	var block []string
	inside := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inside {
				joined := strings.Join(block, "\n")
				if strings.Contains(joined, needle) {
					return joined, true
				}
				block = nil
			}
			inside = !inside
			continue
		}
		if inside {
			block = append(block, line)
		}
	}
	return "", false
}

// The example grid in the READMEs is the first thing a reader sees, and it
// drew a check mark long after the grid stopped drawing one. A check is an
// approval stamp — it reads as "this is fine", which is a grade — and the
// mark that replaced it states a BASIS: our own code ran here.
func TestTheREADMEExampleGridsDrawTheMarkTheGridRenders(t *testing.T) {
	pass, fail, _ := gridMarks(t)
	for _, rel := range publicREADMEs() {
		grid, ok := fencedBlockContaining(readRepoFile(t, rel), "v5.10.0")
		if !ok {
			t.Errorf("%s: no example grid — the language bar links it as the same document", rel)
			continue
		}
		if !strings.Contains(grid, pass) {
			t.Errorf("%s: the example grid never draws %q, the mark buildPivotCell puts on a cell we ran:\n%s",
				rel, pass, grid)
		}
		if strings.Contains(grid, "\u2713") {
			t.Errorf("%s: the example grid still draws a check mark; the grid renders %q for a clean run and %q for a failed one:\n%s",
				rel, pass, fail, grid)
		}
	}
}

// The site's own legend is the second place the glyphs are stated, and the
// one a reader sees beside the real grid. base.html carries them as literals,
// so nothing but a test keeps them equal to the function's output.
func TestTheSiteLegendDrawsTheMarksTheGridRenders(t *testing.T) {
	pass, fail, empty := gridMarks(t)
	tpl := readRepoFile(t, "internal/web/templates/base.html")
	i := strings.Index(tpl, `class="pivotlegend-marks`)
	if i < 0 {
		t.Fatal("base.html has no pivot legend")
	}
	legend := tpl[i:]
	if j := strings.Index(legend, "</ul>"); j >= 0 {
		legend = legend[:j]
	}
	for _, mark := range []string{pass, fail, empty} {
		if !strings.Contains(legend, mark) {
			t.Errorf("the legend never shows %q, which buildPivotCell renders:\n%s", mark, legend)
		}
	}
	if strings.Contains(legend, "\u2713") {
		t.Errorf("the legend shows a check mark, which the grid does not render:\n%s", legend)
	}
}

// Evidence does not decay. internal/compatibility/confidence.go says so in as
// many words and deliberately keeps no age field; buildPivotCell marks
// nothing stale. The READMEs promised the opposite — a 90-day half-life and
// cells that "say so" when they age — which is a description of the trust
// model itself, not a detail.
//
// "90" appeared in these documents for exactly one reason, so its absence is
// the whole check. STABLE's 30-day window is a different number and stays.
func TestNoREADMEPromisesAnEvidenceHalfLife(t *testing.T) {
	for _, rel := range publicREADMEs() {
		for i, line := range strings.Split(readRepoFile(t, rel), "\n") {
			if strings.Contains(line, "90") {
				t.Errorf("%s:%d states a 90-day figure — evidence does not decay and no cell goes stale (internal/compatibility/confidence.go):\n%s",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// staleClaim is one phrase that outgrew what was measured, pinned to the
// document and the key it appeared in.
//
// The words are per-language on purpose. An English fix that leaves eight
// translations claiming a head count is the exact failure this audit found,
// and a table is the only thing that makes the translations fail alongside
// the original.
type staleClaim struct {
	locale  string
	key     string
	phrase  string
	instead string
}

// localeCopyClaims are the claims the site's own strings must not make.
//
// Two vocabularies had drifted. "Runs"/"machines" for an observation count:
// one build files an observation per stage it reached — compile, typecheck,
// test — so the number counts neither builds nor machines nor people, and
// buildPivotCell's own tooltip says "observations" for that reason. And
// "check" for the mark: the grid renders a record mark, and it is not a check
// precisely because a check reads as a verdict.
var localeCopyClaims = []staleClaim{
	{"en", "legend.how_rate", "recorded runs", "recorded observations"},
	{"en", "legend.how_rate", "the check", "the record mark"},
	{"en", "landing.ladder_rule", "recorded runs", "recorded observations"},
	{"en", "landing.matrix_sub", "a recorded run", "a recorded observation"},
	{"en", "landing.what_a", "Recorded runs", "Recorded observations"},
	{"en", "landing.what_a", "real machines", "real environments"},
	{"en", "landing.what_body", "on real machines", "in real environments"},
	{"en", "landing.ladder_stable", "independent peers", "distinct peer keys"},
	{"en", "landing.ladder_cross", "An independent peer", "A peer key other than the origin"},
	{"en", "stats.observations_sub", "builds that really ran", "recorded observations, one per build stage"},

	{"ko", "legend.how_rate", "실행 수", "관측 수"},
	{"ko", "legend.how_rate", "체크", "≡ 표시"},
	{"ko", "landing.ladder_rule", "기록된 실행", "기록된 관측"},
	{"ko", "landing.matrix_sub", "기록된 실행", "기록된 관측"},
	{"ko", "landing.what_a", "실제 머신", "실제 환경"},
	{"ko", "landing.what_body", "실제 머신", "실제 환경"},
	{"ko", "landing.ladder_stable", "독립 피어 3명", "서로 다른 피어 키 3개"},
	{"ko", "landing.ladder_cross", "독립된 다른 피어", "출처와 다른 피어 키"},
	{"ko", "stats.observations_sub", "실제로 돌아간 빌드", "기록된 관측, 빌드 단계마다 하나"},

	{"ja", "legend.how_rate", "実行の数", "観測の数"},
	{"ja", "legend.how_rate", "チェック", "≡ の印"},
	{"ja", "landing.ladder_rule", "記録された実行", "記録済み観測"},
	{"ja", "landing.matrix_sub", "記録された実行", "記録された観測"},
	{"ja", "landing.what_a", "実マシン", "実環境"},
	{"ja", "landing.what_body", "実機", "実際の環境"},
	{"ja", "landing.ladder_stable", "独立したピア 3", "異なるピア鍵 3"},
	{"ja", "landing.ladder_cross", "独立したピア", "出自と異なるピア鍵"},
	{"ja", "stats.observations_sub", "実際に走ったビルド", "記録された観測、ビルド段階ごとに 1 件"},

	{"zh-CN", "legend.how_rate", "运行次数", "观测次数"},
	{"zh-CN", "legend.how_rate", "勾号", "≡ 标记"},
	{"zh-CN", "landing.ladder_rule", "记录的运行", "已记录观测"},
	{"zh-CN", "landing.matrix_sub", "一次已记录的运行", "一条已记录的观测"},
	{"zh-CN", "landing.what_a", "真实机器", "真实环境"},
	{"zh-CN", "landing.what_body", "真实机器", "真实环境"},
	{"zh-CN", "landing.ladder_stable", "三个独立节点", "三个不同的对等端密钥"},
	{"zh-CN", "landing.ladder_cross", "一个独立节点", "一个不同的对等端密钥"},
	{"zh-CN", "stats.observations_sub", "真实运行过的构建", "已记录的观测，每个构建阶段一条"},

	{"es", "legend.how_rate", "ejecuciones registradas", "observaciones registradas"},
	{"es", "landing.ladder_rule", "ejecuciones registradas", "observaciones registradas"},
	{"es", "landing.matrix_sub", "una ejecución registrada", "una observación registrada"},
	{"es", "landing.what_a", "máquinas reales", "entornos reales"},
	{"es", "landing.what_body", "máquinas reales", "entornos reales"},
	{"es", "landing.ladder_stable", "pares independientes", "claves de par distintas"},
	{"es", "landing.ladder_cross", "par independiente", "clave de par distinta"},
	{"es", "stats.observations_sub", "compilaciones que se ejecutaron de verdad", "observaciones registradas, una por etapa"},

	{"fr", "legend.how_rate", "exécutions enregistrées", "observations enregistrées"},
	{"fr", "legend.how_rate", "la coche", "la marque ≡"},
	{"fr", "landing.ladder_rule", "exécutions enregistrées", "observations enregistrées"},
	{"fr", "landing.matrix_sub", "une exécution enregistrée", "une observation enregistrée"},
	{"fr", "landing.what_a", "vraies machines", "vrais environnements"},
	{"fr", "landing.what_body", "vraies machines", "vrais environnements"},
	{"fr", "landing.ladder_stable", "pairs indépendants", "clés de pair distinctes"},
	{"fr", "landing.ladder_cross", "pair indépendant", "clé de pair distincte"},
	{"fr", "stats.observations_sub", "des builds réellement exécutés", "des observations enregistrées, une par étape"},

	{"de", "legend.how_rate", "aufgezeichneten Läufe", "aufgezeichneten Beobachtungen"},
	{"de", "legend.how_rate", "Häkchen", "Zeichen ≡"},
	{"de", "landing.ladder_rule", "erfassten Läufe", "aufgezeichneten Beobachtungen"},
	{"de", "landing.matrix_sub", "ein aufgezeichneter Lauf", "eine aufgezeichnete Beobachtung"},
	{"de", "landing.what_a", "echten Rechnern", "echten Umgebungen"},
	{"de", "landing.what_body", "echten Maschinen", "echten Umgebungen"},
	{"de", "landing.ladder_stable", "unabhängige Peers", "verschiedene Peer-Schlüssel"},
	{"de", "landing.ladder_cross", "unabhängiger Peer", "anderer Peer-Schlüssel"},
	{"de", "stats.observations_sub", "Builds, die wirklich liefen", "aufgezeichnete Beobachtungen, eine je Stufe"},

	{"pt-BR", "legend.how_rate", "execuções registradas", "observações registradas"},
	{"pt-BR", "landing.ladder_rule", "execuções registradas", "observações registradas"},
	{"pt-BR", "landing.matrix_sub", "uma execução registrada", "uma observação registrada"},
	{"pt-BR", "landing.what_a", "máquinas reais", "ambientes reais"},
	{"pt-BR", "landing.what_body", "máquinas reais", "ambientes reais"},
	{"pt-BR", "landing.ladder_stable", "peers independentes", "chaves de peer distintas"},
	{"pt-BR", "landing.ladder_cross", "peer independente", "chave de peer distinta"},
	{"pt-BR", "stats.observations_sub", "builds que realmente rodaram", "observações registradas, uma por etapa"},

	{"ru", "legend.how_rate", "записанные запуски", "записанные наблюдения"},
	{"ru", "legend.how_rate", "галоч", "знак ≡"},
	{"ru", "landing.ladder_rule", "под ней запускам", "записанных наблюдений"},
	{"ru", "landing.matrix_sub", "записанный запуск", "записанное наблюдение"},
	{"ru", "landing.what_a", "реальных машин", "реальных окружений"},
	{"ru", "landing.what_body", "реальных машинах", "реальных окружениях"},
	{"ru", "landing.ladder_stable", "независимых пиров", "различных ключа пиров"},
	{"ru", "landing.ladder_cross", "Независимый пир", "ключ пира, отличный от опубликовавшего"},
	{"ru", "stats.observations_sub", "сборки, которые действительно запускались", "записанные наблюдения, по одному на этап"},
}

func TestTheLocaleCopyDoesNotOutgrowWhatWasMeasured(t *testing.T) {
	root := repoRoot(t)
	loaded := map[string]map[string]string{}
	for _, c := range localeCopyClaims {
		if _, ok := loaded[c.locale]; !ok {
			b, err := os.ReadFile(filepath.Join(root, "internal", "web", "i18n", "locales", c.locale+".json"))
			if err != nil {
				t.Fatalf("%s: %v", c.locale, err)
			}
			var m map[string]string
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("%s: %v", c.locale, err)
			}
			loaded[c.locale] = m
		}
		value, ok := loaded[c.locale][c.key]
		if !ok {
			t.Errorf("%s.json has no %s — the claim table is pinned to keys that exist", c.locale, c.key)
			continue
		}
		if strings.Contains(value, c.phrase) {
			t.Errorf("%s.json %s still says %q; it means %q:\n  %s",
				c.locale, c.key, c.phrase, c.instead, value)
		}
	}
}
