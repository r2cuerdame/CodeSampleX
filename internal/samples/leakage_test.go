package samples

import (
	"strings"
	"testing"
)

func kindsFor(fs []Finding, file string) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		if file == "" || f.File == file {
			out[f.Kind] = true
		}
	}
	return out
}

// TestLeakageScanAllowsHonestSampleContent pins the three false positives
// that blocked real, clean samples from ever being published: funding URLs
// that npm writes into every lockfile, a localhost URL whose port is a
// template placeholder, and an ordinary non-secret env assignment.
func TestLeakageScanAllowsHonestSampleContent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package-lock.json", `{
  "packages": {
    "node_modules/qs": {"funding": {"url": "https://github.com/sponsors/ljharb"}},
    "node_modules/express": {"funding": {"url": "https://opencollective.com/express"}},
    "node_modules/axios": {"resolved": "https://registry.npmjs.org/axios/-/axios-1.12.2.tgz"}
  }
}`)
	writeFile(t, dir, "contract.mjs", "const res = await fetch(`http://127.0.0.1:${port}/items`);\nprocess.env.TZ = 'Asia/Seoul';\nconst user = { email: 'dev@example.com' };\n")

	fs, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Errorf("clean sample content flagged as leaks: %+v", fs)
	}
}

// Secret-shaped env assignments must still be caught.
func TestLeakageScanFlagsSecretEnvAssignment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "boot.js", `process.env.API_TOKEN = "abc123";`)
	writeFile(t, dir, "boot.py", `os.environ["DB_PASSWORD"] = "hunter2"`)

	fs, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"boot.js", "boot.py"} {
		if !kindsFor(fs, f)[KindEnvAssignment] {
			t.Errorf("secret env assignment in %s not flagged: %+v", f, fs)
		}
	}
}

func TestLeakageScanFlagsSecrets(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "aws.txt", "key=AKIAIOSFODNN7EXAMPLE")
	writeFile(t, dir, "gh.js", `const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789";`)
	writeFile(t, dir, "pat.txt", "github_pat_11AAA5PYI0abcdefGHIJ")
	writeFile(t, dir, "openai.py", `client = OpenAI(api_key="sk-`+strings.Repeat("b", 24)+`")`)
	writeFile(t, dir, "google.txt", "AIza"+strings.Repeat("a", 35))
	writeFile(t, dir, "key.pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIB\n-----END RSA PRIVATE KEY-----")

	fs, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for file, kind := range map[string]string{
		"aws.txt":    KindAWSKey,
		"gh.js":      KindGitHubToken,
		"pat.txt":    KindGitHubToken,
		"openai.py":  KindAPIKey,
		"google.txt": KindGoogleKey,
		"key.pem":    KindPrivateKey,
	} {
		if !kindsFor(fs, file)[kind] {
			t.Errorf("expected %s finding in %s; got %+v", kind, file, fs)
		}
	}
}

func TestLeakageScanFlagsPathsEmailsEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "winpath.md", `see C:\Users\bob\proj\x.js for details`)
	writeFile(t, dir, "unixpath.md", `see /home/bob/proj/x.js for details`)
	writeFile(t, dir, "mail.md", "reach me at dev.lead@somecorp.io")
	writeFile(t, dir, "env.js", `process.env.API_KEY = "abc123"`)
	writeFile(t, dir, "env.py", `os.environ["TOKEN"] = "xyz"`)

	fs, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for file, kind := range map[string]string{
		"winpath.md":  KindAbsolutePath,
		"unixpath.md": KindAbsolutePath,
		"mail.md":     KindEmail,
		"env.js":      KindEnvAssignment,
		"env.py":      KindEnvAssignment,
	} {
		if !kindsFor(fs, file)[kind] {
			t.Errorf("expected %s finding in %s; got %+v", kind, file, fs)
		}
	}
	// Line numbers are 1-based and point at the planted line.
	for _, f := range fs {
		if f.Line != 1 {
			t.Errorf("finding %s/%s line %d, want 1", f.File, f.Kind, f.Line)
		}
		if f.Excerpt == "" {
			t.Errorf("finding %s/%s missing excerpt", f.File, f.Kind)
		}
	}
}

func TestLeakageScanURLAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ok.md", strings.Join([]string{
		"https://registry.npmjs.org/axios",
		"https://pypi.org/pypi/requests/json",
		"https://crates.io/api/v1/crates/serde",
		"https://proxy.golang.org/github.com/a/b/@latest",
		"https://sub.example.com/docs",
		"http://localhost:3000/health",
		"http://127.0.0.1:8080/x",
		"https://codesamplex.dev/schemas/v1",
	}, "\n"))
	writeFile(t, dir, "bad.js", `fetch("https://api.mycompany.io/v2/users")`)

	fs, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if kindsFor(fs, "ok.md")[KindURL] {
		t.Errorf("allowlisted URLs were flagged: %+v", fs)
	}
	if !kindsFor(fs, "bad.js")[KindURL] {
		t.Errorf("non-allowlisted URL not flagged: %+v", fs)
	}
}

func TestLeakageScanProjectIdentifiers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes.md", "ported from SuperSecretProj helper")
	writeFile(t, dir, "origin.md", "originally lived in corp-monorepo")
	writeFile(t, dir, "clean.md", "a perfectly generic sample")

	fs, err := Scan(dir, ScanOptions{ProjectDirName: "SuperSecretProj", GitRemoteName: "corp-monorepo"})
	if err != nil {
		t.Fatal(err)
	}
	if !kindsFor(fs, "notes.md")[KindProjectName] {
		t.Errorf("project dir name not flagged: %+v", fs)
	}
	if !kindsFor(fs, "origin.md")[KindProjectName] {
		t.Errorf("git remote repo name not flagged: %+v", fs)
	}
	if len(kindsFor(fs, "clean.md")) != 0 {
		t.Errorf("clean file wrongly flagged: %+v", fs)
	}

	// Without options the same names are not scanned for.
	fs2, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if kindsFor(fs2, "")[KindProjectName] {
		t.Errorf("project-name findings without options: %+v", fs2)
	}
}

func TestLeakageScanCleanSample(t *testing.T) {
	dir := sampleFixture(t)
	fs, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Fatalf("clean fixture produced findings: %+v", fs)
	}
}
