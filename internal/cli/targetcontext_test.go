package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/daemon"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
)

// The scanner has detected bun.lock / deno.json / an electron dependency
// since the beginning, and nothing in the codebase ever read the result.
// The doc comment says it "is used only to widen search environments" — a
// Bun project was searched as plain node, and execution-context grading,
// which exists precisely to separate those, never saw a difference.
func TestBunProjectIsDetectedAsItsOwnExecutionContext(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"name":"app","dependencies":{"axios":"^1.12.0"}}`)
	write("bun.lock", "{}")

	res, err := evidence.Scan(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TargetContext != "bun" {
		t.Fatalf("targetContext = %q, want bun", res.TargetContext)
	}
	// The scan's own environment describes the machine, not the target —
	// which is why the hint has to be applied on top of it.
	if res.Env.ExecutionContext == "bun" {
		t.Skip("the scan environment already reports bun; the hint is redundant here")
	}
}

func TestMainSearchWorkspaceMemberUsesRootBunTargetContext(t *testing.T) {
	home := newCLIHome(t, nil)

	var (
		mu   sync.Mutex
		reqs []domain.SearchRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/local/v1/status":
			_ = json.NewEncoder(w).Encode(daemon.StatusInfo{
				SchemaVersion: 1,
				Version:       Version,
				Home:          home,
			})
		case "/local/v1/search":
			var req domain.SearchRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			reqs = append(reqs, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(daemon.LocalSearchResponse{
				SearchResponse: domain.SearchResponse{
					SchemaVersion: 1,
					Miss:          true,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://") + "\n"
	if err := os.WriteFile(filepath.Join(home, "daemon.addr"), []byte(addr), 0o600); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	writeFiles := func(files map[string]string) {
		t.Helper()
		for rel, body := range files {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFiles(map[string]string{
		"package.json": `{"name":"repo","private":true,"workspaces":["apps/*"]}`,
		"bun.lock": `{
			"lockfileVersion": 1,
			"workspaces": {
				"": {},
				"apps/web": {"dependencies": {"axios": "^1.12.0"}},
				"apps/denoapp": {"dependencies": {"axios": "^1.12.0"}}
			},
			"packages": {
				"axios": ["axios@1.12.0", "", {}, "sha512-a"]
			}
		}`,
		"apps/web/package.json":     `{"name":"web","dependencies":{"axios":"^1.12.0"}}`,
		"apps/denoapp/package.json": `{"name":"denoapp","dependencies":{"axios":"^1.12.0"}}`,
		"apps/denoapp/deno.json":    `{"tasks":{"dev":"deno run main.ts"}}`,
		"tools/script/package.json": `{"name":"script","dependencies":{"axios":"^1.12.0"}}`,
	})

	runSearchIn := func(t *testing.T, dir string) domain.SearchRequest {
		t.Helper()
		t.Chdir(dir)
		mu.Lock()
		startIdx := len(reqs)
		mu.Unlock()

		out, code := captureStdout(t, func() int {
			return Main([]string{"search", "upload", "multipart", "form", "with", "axios"})
		})
		if code != 0 {
			t.Fatalf("Main(search) exit = %d, output:\n%s", code, out)
		}

		mu.Lock()
		defer mu.Unlock()
		if len(reqs) != startIdx+1 {
			t.Fatalf("captured %d search requests, want %d", len(reqs)-startIdx, 1)
		}
		return reqs[startIdx]
	}

	t.Run("declared workspace member sends bun", func(t *testing.T) {
		member := filepath.Join(root, "apps", "web")
		res, err := evidence.Scan(context.Background(), member, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Env.ExecutionContext == "bun" {
			t.Fatalf("ScanResult.Env.ExecutionContext = %q, must stay non-bun", res.Env.ExecutionContext)
		}
		req := runSearchIn(t, member)
		if req.Environment.ExecutionContext != "bun" {
			t.Errorf("SearchRequest.Environment.ExecutionContext = %q, want bun", req.Environment.ExecutionContext)
		}
	})

	t.Run("undeclared folder does not inherit bun", func(t *testing.T) {
		req := runSearchIn(t, filepath.Join(root, "tools", "script"))
		if req.Environment.ExecutionContext == "bun" {
			t.Errorf("undeclared folder inherited bun ExecutionContext: %q", req.Environment.ExecutionContext)
		}
	})

	t.Run("member local deno overrides workspace bun", func(t *testing.T) {
		req := runSearchIn(t, filepath.Join(root, "apps", "denoapp"))
		if req.Environment.ExecutionContext != "deno" {
			t.Errorf("SearchRequest.Environment.ExecutionContext = %q, want deno", req.Environment.ExecutionContext)
		}
	})
}
