package samples

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// genericDirNames are directory names that identify nobody. Treating one of
// these as the contributor's project name would flag the word "src" or
// "app" everywhere it appears and reject every honest sample.
var genericDirNames = map[string]bool{
	"src": true, "app": true, "lib": true, "test": true, "tests": true,
	"tmp": true, "temp": true, "work": true, "code": true, "dev": true,
	"projects": true, "repos": true, "workspace": true, "home": true,
	"desktop": true, "documents": true, "downloads": true, "new": true,
	"sample": true, "samples": true, "example": true, "examples": true,
	"main": true, "master": true, "root": true, "build": true, "out": true,
}

// ProvenanceOptions derives the project-identifying names to scan for, from
// the directory the contributor is working in and its git remote.
//
// These two fields are what the whole KindProjectName check is built on —
// "a sample mentioning either leaks provenance" — and they were never set.
// Every call site passed ScanOptions{}, so the check compiled no patterns
// and matched nothing, at create time and at the publish gate alike. A
// sample carrying `// part of the acme-billing-core monorepo` published
// clean, and an employer's internal project name went to a public network.
//
// Names too short or too generic are dropped: someone working in ~/src or
// ~/app would otherwise have every occurrence of that word flagged, which
// rejects honest samples to catch nothing.
func ProvenanceOptions(dir string) ScanOptions {
	var opts ScanOptions
	if abs, err := filepath.Abs(dir); err == nil {
		if name := filepath.Base(abs); usableProjectName(name) {
			opts.ProjectDirName = name
		}
	}
	if remote := gitRemoteName(dir); usableProjectName(remote) {
		opts.GitRemoteName = remote
	}
	return opts
}

func usableProjectName(name string) bool {
	name = strings.TrimSpace(name)
	return len(name) >= 4 && !genericDirNames[strings.ToLower(name)]
}

// gitRemoteName returns the repository name from the origin remote, or "".
// A failure is not an error: plenty of directories are not repositories,
// and the directory name alone is still worth scanning for.
func gitRemoteName(dir string) string {
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(out))
	url = strings.TrimSuffix(url, ".git")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		url = url[i+1:]
	}
	return url
}
