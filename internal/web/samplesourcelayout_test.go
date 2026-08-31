package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The source is the substance of a sample page and must get the wide column.
//
// Files and source share a two-column grid. The split was written when the
// second cell held something small, and the source moved in later: production
// gave 1.15fr to a list of seven short filenames and 0.85fr to the code, so on
// a 1568px screen the code read through a ~200px window with a horizontal
// scrollbar under it. That is not "inspect the source without downloading the
// archive"; it is the archive with extra steps.
//
// Measured rather than read off the stylesheet, because the question is what
// the rendered boxes end up being, and fr units answer it only together with
// the content in them.
func TestTheSourcePaneIsWiderThanTheFileList(t *testing.T) {
	chrome := findChrome(t)
	mux, store := newTestMux(t, nil)
	store.sampleSource = map[string][]SampleFile{
		"sha256:d1e2f3": {
			{Name: "csx.json", Body: `{"schemaVersion":1}`},
			{Name: "sample.go", Body: "package main\n\n" +
				"// A line of about the width real sample code actually reaches.\n" +
				"func main() { fmt.Println(\"hello from a sample that has real content\") }\n"},
		},
	}
	srv := httptest.NewServer(sourceHarness(mux, "/samples/sha256:d1e2f3"))
	defer srv.Close()

	var reports []sourceLayoutReport
	payload := renderMeasurement(t, chrome, srv.URL+sourceMeasurePath)
	if err := json.Unmarshal([]byte(payload), &reports); err != nil {
		t.Fatalf("measurement %q: %v", payload, err)
	}
	for _, r := range reports {
		if !r.Found {
			t.Errorf("viewport %dpx: the source or file panel is missing", r.Width)
			continue
		}
		if r.Width >= 1000 && r.Source <= r.Files {
			t.Errorf("viewport %dpx: source pane is %.0fpx and the file list is %.0fpx — the code has the narrow column",
				r.Width, r.Source, r.Files)
		}
		// And it is not merely wider by a hair: code needs most of the row.
		if r.Width >= 1000 && r.Source < (r.Source+r.Files)*0.6 {
			t.Errorf("viewport %dpx: source pane is %.0fpx of %.0fpx across — too narrow to read code in",
				r.Width, r.Source, r.Source+r.Files)
		}
	}
}

var sourceViewports = []int{1280, 900}

type sourceLayoutReport struct {
	Width  int     `json:"width"`
	Found  bool    `json:"found"`
	Files  float64 `json:"files"`
	Source float64 `json:"source"`
}

const sourceMeasurePath = "/__sample-source-measure"

func sourceHarness(mux *http.ServeMux, target string) http.Handler {
	widths := make([]string, 0, len(sourceViewports))
	frames := make([]string, 0, len(sourceViewports))
	for _, w := range sourceViewports {
		widths = append(widths, fmt.Sprint(w))
		frames = append(frames, fmt.Sprintf(
			`<iframe id="s%d" width="%d" height="900" src="%s"></iframe>`, w, w, html.EscapeString(target)))
	}
	page := strings.NewReplacer(
		"__WIDTHS__", strings.Join(widths, ","),
		"__FRAMES__", strings.Join(frames, "\n"),
	).Replace(sourceHarnessHTML)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != sourceMeasurePath {
			mux.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
}

const sourceHarnessHTML = `<!doctype html>
<meta charset="utf-8"><title>sample source measure</title>
<body style="margin:0">
__FRAMES__
<pre id="measure">PENDING</pre>
<script>
function run(){
  var out = [];
  var widths = [__WIDTHS__];
  for (var i = 0; i < widths.length; i++) {
    var w = widths[i], fr = document.getElementById('s' + w);
    var doc = fr.contentDocument;
    var src = doc.querySelector('.sample-source');
    var files = doc.querySelector('.sample-detail__file-list');
    var filesCard = files ? files.closest('.sample-detail__card') : null;
    out.push({width: w, found: !!(src && filesCard),
              files: filesCard ? filesCard.getBoundingClientRect().width : 0,
              source: src ? src.getBoundingClientRect().width : 0});
  }
  document.getElementById('measure').textContent = JSON.stringify(out);
}
window.addEventListener('load', function(){ setTimeout(run, 400); });
</script>
`
