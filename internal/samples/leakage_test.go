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
	// RFC 2606 reserved names are what documentation is supposed to use.
	writeFile(t, dir, "contract.mjs", "const res = await fetch(`http://127.0.0.1:${port}/items`);\n"+
		"process.env.TZ = 'Asia/Seoul';\n"+
		"const user = { email: 'dev@example.com' };\n"+
		"const api = 'https://api.example.test/v1';\n"+
		"const alt = 'https://docs.example.org/guide';\n")

	// Maintainer funding hosts are unbounded (dotenvx.com, feross.org, …),
	// so lockfile URLs are not allowlisted at all.
	writeFile(t, dir, "pnpm-lock.yaml", "  resolution: {integrity: sha512-x}\n  funding: https://dotenvx.com\n")

	fs, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Errorf("clean sample content flagged as leaks: %+v", fs)
	}
}

// A credential inside a lockfile URL is a real leak and must survive the
// lockfile exemption.
func TestLeakageScanFlagsCredentialsInLockfileURL(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package-lock.json",
		`{"packages":{"node_modules/x":{"resolved":"https://ci-user:s3cr3t@registry.internal.acme.com/x.tgz"}}}`)

	fs, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !kindsFor(fs, "package-lock.json")[KindURL] {
		t.Errorf("credentialed registry URL in a lockfile not flagged: %+v", fs)
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

// A lockfile copies each dependency's author block straight out of the
// registry, so the addresses in it belong to the library's maintainers and
// are already public. Treating them as leaks blocked every PHP sample: one
// composer.lock for php-parser plus phpunit carries 38 of them, and the
// refusal is unoverridable by design.
func TestLeakageScanAllowsMaintainerEmailsInLockfiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.lock",
		`{"packages":[{"name":"nikic/php-parser","authors":[{"name":"Nikita Popov","email":"nikic@php.net"}]}]}`)
	writeFile(t, dir, "Gemfile.lock", "  maintainer: someone@rubygems-author.example.org\n")

	fs, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Kind == KindEmail {
			t.Errorf("maintainer address in a lockfile flagged: %+v", f)
		}
	}
}

// The exemption is for addresses only. A lockfile is still scanned for the
// things a contributor can actually leak into one.
func TestLeakageScanStillFlagsSecretsInLockfiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.lock",
		"{\"x\":\"AKIAIOSFODNN7EXAMPLE\"}\n-----BEGIN RSA PRIVATE KEY-----\n")

	kinds := kindsFor(mustScan(t, dir), "composer.lock")
	if !kinds[KindAWSKey] {
		t.Error("AWS key in a lockfile not flagged")
	}
	if !kinds[KindPrivateKey] {
		t.Error("private key in a lockfile not flagged")
	}
}

// An address in hand-written source is not covered by the exemption: that
// one really can be the contributor's.
func TestLeakageScanStillFlagsEmailsOutsideLockfiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/app.js", "// contact: someone@acme-internal.co\n")

	if !kindsFor(mustScan(t, dir), "src/app.js")[KindEmail] {
		t.Error("address in source not flagged")
	}
}

func mustScan(t *testing.T, dir string) []Finding {
	t.Helper()
	fs, err := Scan(dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// A backslash-separated identifier in JSON is not a UNC path. composer.lock
// stores PHP class names with the backslashes doubled by JSON escaping, and
// the middle of "Monolog\\Log\\Logger" matched the UNC pattern, so a sample
// was refused for having namespaces.
func TestLeakageScanDoesNotReadEscapedNamespacesAsUNCPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.lock",
		`{"autoload":{"psr-4":{"Monolog\\":"src/"}},"class":"Monolog\\Log\\Logger"}`)
	writeFile(t, dir, "src/App.php", "use Monolog\\Handler\\StreamHandler;")

	for _, f := range mustScan(t, dir) {
		if f.Kind == KindAbsolutePath {
			t.Errorf("PHP namespace read as a path: %+v", f)
		}
	}
}

// A real UNC path still is one. It begins a value, which is exactly what
// separates it from the case above.
func TestLeakageScanStillFlagsRealUNCPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes.md", `build output goes to \\fileserver\builds\evening`)
	writeFile(t, dir, "config.json", `{"share": "\\\\buildbox\\artifacts"}`)

	for _, f := range []string{"notes.md", "config.json"} {
		if !kindsFor(mustScan(t, dir), f)[KindAbsolutePath] {
			t.Errorf("UNC path in %s not flagged", f)
		}
	}
}

// The distribution's own trees are not a leak. A sample about musl versus
// glibc has to name /usr/lib/x86_64-linux-gnu/libc.so.6, and that path is
// byte-identical on every machine running that distribution — it describes
// the platform, not the author. It was blocking a published sample.
func TestLeakageScanAllowsDistributionOwnedPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes.md",
		"the loader searches /usr/lib and /usr/local/lib; the file is /usr/lib/x86_64-linux-gnu/libc.so.6\nconfig lives in /etc/ssl/certs\n")

	for _, f := range mustScan(t, dir) {
		if f.Kind == KindAbsolutePath {
			t.Errorf("distribution path flagged: %+v", f)
		}
	}
}

// The paths that can carry a person, a project or an employer still are.
func TestLeakageScanStillFlagsIdentifyingPaths(t *testing.T) {
	for _, path := range []string{
		"/home/bob/proj/x.js",
		"/Users/alice/work/secret-client/main.go",
		"/opt/acme-corp/deploy.sh",
		"/var/lib/internal-billing/db",
		"/root/.ssh/id_ed25519",
	} {
		dir := t.TempDir()
		writeFile(t, dir, "notes.md", "see "+path+" for details")
		if !kindsFor(mustScan(t, dir), "notes.md")[KindAbsolutePath] {
			t.Errorf("%s not flagged", path)
		}
	}
}

// testserver is the host a test client invents for a request that never
// leaves the process. Starlette and Django both use it, so it appears in
// every FastAPI test in the world and identifies nobody.
func TestLeakageScanAllowsTestClientSentinelHost(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "test/contract.py", "assert resp.request.url == \"http://testserver/items/7\"\n")

	for _, f := range mustScan(t, dir) {
		if f.Kind == KindURL {
			t.Errorf("test client sentinel host flagged: %+v", f)
		}
	}
}

// A test that asserts on a URL writes it as a pattern, and the escapes made
// an allowlisted host match nothing: %r{https://api\.example\.com/items}
// was refused for naming example.com. A hostname cannot contain a backslash,
// so one there is always escaping.
func TestLeakageScanUnescapesHostsWrittenAsPatterns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "test/contract.rb",
		`assert_match(%r{https://api\.example\.com/items}, err.message)`)
	writeFile(t, dir, "test/contract.js",
		`expect(url).toMatch(/https:\/\/api\.example\.com/)`)

	for _, f := range mustScan(t, dir) {
		if f.Kind == KindURL {
			t.Errorf("allowlisted host written as a pattern was flagged: %+v", f)
		}
	}
}

// Unescaping must not become a way through: a host that is not allowlisted
// is still caught when it is written as a regex.
func TestLeakageScanStillFlagsUnknownHostsWrittenAsPatterns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "test/contract.rb",
		`assert_match(%r{https://wiki\.internal-acme\.io/runbook}, msg)`)

	if !kindsFor(mustScan(t, dir), "test/contract.rb")[KindURL] {
		t.Error("an unknown host written as a regex was not flagged")
	}
}

// A specification identifier is not an address. Every JSON Schema a tool
// generates carries $schema, and nothing fetches it — it names a draft the
// way an SPDX id names a licence. It refused a sample for containing the
// schema its own dependency emitted.
func TestLeakageScanAllowsSpecificationIdentifiers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "test/contract.mjs",
		`assert.equal(schema.$schema, "http://json-schema.org/draft-07/schema#");`)
	writeFile(t, dir, "src/doc.xml",
		`<rdf xmlns="http://www.w3.org/1999/02/22-rdf-syntax-ns#" />`)

	for _, f := range mustScan(t, dir) {
		if f.Kind == KindURL {
			t.Errorf("specification identifier flagged: %+v", f)
		}
	}
}

// An unbraced interpolation runs straight on past the host, so the host
// extractor swallowed it: 'http://example.com$path' in Dart read as the host
// "example.com$path", which matched nothing in the allowlist and refused a
// sample for naming the documentation domain. Shell, PHP and Ruby write it
// the same way.
func TestLeakageScanAllowsAHostFollowedByAnInterpolation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "lib/api.dart", `final uri = Uri.parse('http://example.com$path');`)
	writeFile(t, dir, "run.sh", `curl "http://localhost:$PORT/health"`)

	for _, f := range mustScan(t, dir) {
		if f.Kind == KindURL {
			t.Errorf("allowlisted host before an interpolation was flagged: %+v", f)
		}
	}
}

// Cutting at the '$' must not turn an unknown host into an allowed one, and
// a host that IS the interpolation stays unknown.
func TestLeakageScanStillFlagsInterpolatedUnknownHosts(t *testing.T) {
	for _, line := range []string{
		`final uri = Uri.parse('http://wiki.internal-acme.io$path');`,
		`final uri = Uri.parse('http://$secretHost/admin');`,
	} {
		dir := t.TempDir()
		writeFile(t, dir, "lib/api.dart", line)
		if !kindsFor(mustScan(t, dir), "lib/api.dart")[KindURL] {
			t.Errorf("not flagged: %s", line)
		}
	}
}
