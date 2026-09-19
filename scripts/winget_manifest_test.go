package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestWinGetManifestFilesExist(t *testing.T) {
	manifestDir := filepath.Join("..", "packaging", "winget", "manifests", "r", "r2cuerdame", "CodeSampleX", "0.1.134")
	files := []string{
		"r2cuerdame.CodeSampleX.yaml",
		"r2cuerdame.CodeSampleX.installer.yaml",
		"r2cuerdame.CodeSampleX.locale.en-US.yaml",
	}

	for _, file := range files {
		path := filepath.Join(manifestDir, file)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("required WinGet manifest file missing: %s (%v)", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("WinGet manifest file is empty: %s", path)
		}
	}
}

func TestWinGetManifestContentIntegrity(t *testing.T) {
	manifestDir := filepath.Join("..", "packaging", "winget", "manifests", "r", "r2cuerdame", "CodeSampleX", "0.1.134")

	versionPath := filepath.Join(manifestDir, "r2cuerdame.CodeSampleX.yaml")
	installerPath := filepath.Join(manifestDir, "r2cuerdame.CodeSampleX.installer.yaml")
	localePath := filepath.Join(manifestDir, "r2cuerdame.CodeSampleX.locale.en-US.yaml")

	versionBytes, err := os.ReadFile(versionPath)
	if err != nil {
		t.Fatalf("read version manifest: %v", err)
	}
	installerBytes, err := os.ReadFile(installerPath)
	if err != nil {
		t.Fatalf("read installer manifest: %v", err)
	}
	localeBytes, err := os.ReadFile(localePath)
	if err != nil {
		t.Fatalf("read locale manifest: %v", err)
	}

	versionContent := string(versionBytes)
	installerContent := string(installerBytes)
	localeContent := string(localeBytes)

	// Ensure no UTF-8 BOM (0xEF, 0xBB, 0xBF) and exact schema header on the first line
	const schemaHeaderPrefix = "# yaml-language-server: $schema=https://aka.ms/winget-manifest."
	for name, raw := range map[string][]byte{
		"version":   versionBytes,
		"installer": installerBytes,
		"locale":    localeBytes,
	} {
		if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
			t.Errorf("%s manifest contains a UTF-8 byte order mark (BOM); WinGet manifests must be UTF-8 without BOM", name)
		}
		firstLine := strings.SplitN(string(raw), "\n", 2)[0]
		firstLine = strings.TrimRight(firstLine, "\r")
		if !strings.HasPrefix(firstLine, schemaHeaderPrefix) {
			t.Errorf("%s manifest must begin with schema header %q, got %q", name, schemaHeaderPrefix, firstLine)
		}
	}

	// Check PackageIdentifier consistency
	const expectedPkgID = "PackageIdentifier: r2cuerdame.CodeSampleX"
	for name, content := range map[string]string{
		"version":   versionContent,
		"installer": installerContent,
		"locale":    localeContent,
	} {
		if !strings.Contains(content, expectedPkgID) {
			t.Errorf("%s manifest does not contain expected %q", name, expectedPkgID)
		}
	}

	// Check PackageVersion consistency
	const expectedVersion = "PackageVersion: 0.1.134"
	for name, content := range map[string]string{
		"version":   versionContent,
		"installer": installerContent,
		"locale":    localeContent,
	} {
		if !strings.Contains(content, expectedVersion) {
			t.Errorf("%s manifest does not contain expected %q", name, expectedVersion)
		}
	}

	// Check installer manifest fields
	if !strings.Contains(installerContent, "InstallerType: portable") {
		t.Errorf("installer manifest must specify InstallerType: portable")
	}
	if !strings.Contains(installerContent, "- csx") {
		t.Errorf("installer manifest must register command 'csx'")
	}
	if !strings.Contains(installerContent, "Architecture: x64") || !strings.Contains(installerContent, "Architecture: arm64") {
		t.Errorf("installer manifest must support both x64 and arm64")
	}

	sha256Regex := regexp.MustCompile(`InstallerSha256:\s*([A-F0-9]{64})`)
	matches := sha256Regex.FindAllStringSubmatch(installerContent, -1)
	if len(matches) != 2 {
		t.Fatalf("expected 2 SHA256 checksums in installer manifest, found %d", len(matches))
	}

	// Check locale manifest fields
	expectedFields := []string{
		"Publisher: r2cuerdame",
		"PackageName: CodeSampleX",
		"Moniker: csx",
		"License: Apache-2.0",
		"PackageLocale: en-US",
		"PackageUrl: https://codesamplex.dev",
		"PublisherUrl: https://github.com/r2cuerdame",
		"PublisherSupportUrl: https://github.com/r2cuerdame/CodeSampleX/issues",
		"LicenseUrl: https://github.com/r2cuerdame/CodeSampleX/blob/main/LICENSE",
	}
	for _, field := range expectedFields {
		if !strings.Contains(localeContent, field) {
			t.Errorf("locale manifest missing expected field %q", field)
		}
	}
}

func TestWinGetManifestMatchesKnownReleaseEvidence(t *testing.T) {
	manifestDir := filepath.Join("..", "packaging", "winget", "manifests", "r", "r2cuerdame", "CodeSampleX", "0.1.134")
	installerPath := filepath.Join(manifestDir, "r2cuerdame.CodeSampleX.installer.yaml")
	installerBytes, err := os.ReadFile(installerPath)
	if err != nil {
		t.Fatalf("read installer manifest: %v", err)
	}
	content := string(installerBytes)

	// SHA-256 values from v0.1.134 SHA256SUMS.txt
	const wantX64Sha256 = "B4D567E9C992973FE41DAAE137C16D70F7CA2A285EB582563B2B8A310BD559CB"
	const wantArm64Sha256 = "374F7D928831276D6A819C8F9EECB3F9C3D5989760CD7D17501F1295573CABEE"

	if !strings.Contains(content, wantX64Sha256) {
		t.Errorf("installer manifest does not contain expected x64 SHA256 %s", wantX64Sha256)
	}
	if !strings.Contains(content, wantArm64Sha256) {
		t.Errorf("installer manifest does not contain expected arm64 SHA256 %s", wantArm64Sha256)
	}
}

func TestWinGetCliValidation(t *testing.T) {
	wingetPath, err := exec.LookPath("winget")
	if err != nil {
		t.Skip("winget CLI not found in PATH; skipping local schema validator invocation")
	}

	manifestDir, err := filepath.Abs(filepath.Join("..", "packaging", "winget", "manifests", "r", "r2cuerdame", "CodeSampleX", "0.1.134"))
	if err != nil {
		t.Fatalf("resolve manifest directory: %v", err)
	}

	cmd := exec.Command(wingetPath, "validate", "--manifest", manifestDir)
	out, err := cmd.CombinedOutput()
	outputStr := string(out)

	if err != nil {
		t.Fatalf("winget validate failed: %v\nOutput:\n%s", err, outputStr)
	}

	if strings.Contains(outputStr, "Manifest Warning:") {
		t.Fatalf("winget validate reported warnings:\n%s", outputStr)
	}

	if !strings.Contains(outputStr, "validation succeeded") && !strings.Contains(outputStr, "succeeded") {
		t.Fatalf("expected validation success message, got:\n%s", outputStr)
	}
}
