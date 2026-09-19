# WinGet Distribution Evidence & External Manual Gate Record

- **Issue:** #189 (Distribution: publish CodeSampleX through WinGet)
- **Date Recorded:** 2026-09-19
- **Package Identifier:** `r2cuerdame.CodeSampleX`
- **Moniker:** `csx`
- **Package Version:** `0.1.134`
- **Manifest Schema Version:** `1.12.0`
- **Upstream Repository:** `microsoft/winget-pkgs`
- **Upstream Pull Request:** [#429928](https://github.com/microsoft/winget-pkgs/pull/429928)
- **Fork Branch:** `r2cuerdame:csx-0.1.134` (commit `67a835b4470d84116a916ea34c40e6d38abf9d22`)
- **Status:** **All repository-side work and upstream CI validations complete; awaiting external Microsoft moderator approval (manual gate).**

---

## 1. Asset & Provenance Reconciliation

The manifest targets stable, immutable GitHub Release assets for release `v0.1.134`:

| Architecture | Release Asset URL | SHA-256 Checksum | Size |
|---|---|---|---|
| `x64` | `https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.134/csx-windows-amd64.exe` | `B4D567E9C992973FE41DAAE137C16D70F7CA2A285EB582563B2B8A310BD559CB` | 17,265,664 B |
| `arm64` | `https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.134/csx-windows-arm64.exe` | `374F7D928831276D6A819C8F9EECB3F9C3D5989760CD7D17501F1295573CABEE` | 15,962,624 B |

Both checksums match the official immutable release `SHA256SUMS.txt` published on GitHub Releases.

### Installer Behavior
- **InstallerType:** `portable`
- **Commands:** `csx`
- WinGet places portable executables in `%LOCALAPPDATA%\Microsoft\WinGet\Packages` and links them into `%LOCALAPPDATA%\Microsoft\WinGet\Links` (which WinGet manages in the user PATH).
- Clean silent install and clean uninstall are handled natively by WinGet portable architecture without registry pollution.

---

## 2. Issue #70 Reconciliation (Defender False-Positive Mitigation)

Issue #70 identified heuristic false-positive quarantines (`Trojan:Win32/Bearfoos.*!ml`) on certain release builds. Per the Issue #189 safety and gating contract:
- The WinGet manifest was constructed against release `v0.1.134`, which was scanned with Microsoft Defender Antivirus (definition 1.459.55.0) and confirmed clean (0 detections).
- PR #186 landed on main to guarantee that if Defender ever locks payload files (Win32 error 225 / `ERROR_VIRUS_INFECTED`), the launcher self-recovers via official signed asset rehydration rather than crashing or stranding the user.
- The upstream WinGet automated validation job `07. Installers Scan` executed on the actual release binaries and passed with 0 security detections.

---

## 3. Local Schema Validation Evidence

Windows Package Manager client `v1.29.380` executed local schema validation against the repository manifest directory:

```text
winget validate --manifest packaging\winget\manifests\r\r2cuerdame\CodeSampleX\0.1.134
Output: Manifest validation succeeded.
Exit code: 0
```

Automated Go test `scripts/winget_manifest_test.go` enforces:
1. Manifest existence in `packaging/winget/manifests/r/r2cuerdame/CodeSampleX/0.1.134/`
2. PackageIdentifier (`r2cuerdame.CodeSampleX`) and PackageVersion (`0.1.134`) consistency
3. InstallerType (`portable`), Commands (`csx`), x64/arm64 architecture entries
4. SHA-256 checksum agreement with official release evidence
5. Complete locale metadata (license, URLs, tags, description, moniker)
6. Automatic `winget validate` execution when `winget` is in PATH

---

## 4. Upstream WinGet PR & CI Evidence

- **Pull Request:** https://github.com/microsoft/winget-pkgs/pull/429928
- **Title:** `New package: r2cuerdame.CodeSampleX version 0.1.134`
- **Base:** `microsoft:master` <- **Head:** `r2cuerdame:csx-0.1.134`
- **Labels:** `Azure-Pipeline-Passed`, `New-Package`, `Validation-Completed`
- **Auto-Merge:** Enabled (squash merge via `microsoft-github-policy-service`)

### Automated Pipeline Results

| Job Name | Status | Duration |
|---|---|---|
| `01. Pull Request Validation` | PASS | 12s |
| `02. Manifest Validation` | PASS | 8s |
| `03. URLs Validation` | PASS | 17s |
| `04. URL Domain Validation` | PASS | 9s |
| `05. Manifest Policy Validation` | PASS | 27s |
| `06. Catalog Content Verification` | PASS | 1m 24s |
| `07. Installers Scan` | PASS | 5m 41s |
| `08. Installation Validation` (Windows sandbox) | PASS | 43m 42s |
| `09. Installer Metadata Validation` | PASS | 5s |
| `10. Validation Completed` | PASS | 8s |
| `license/cla` | PASS | signed |

All 10 automated validation steps succeeded without errors or warnings.

---

## 5. External Manual Gate Specification

Under `microsoft/winget-pkgs` repository policy, every pull request introducing a new package (`New-Package`) requires explicit review and approval by a community moderator before merging:
> *"The check-in policies require a moderator to approve PRs from the community. Our moderators are community volunteers, please be patient and allow them sufficient time to review your submission. Template: msftbot/requiresApproval/moderator"*

This is an **external, manual gate**:
- The repository-side manifest authoring, release reconciliation, local validation, fork preparation, upstream PR creation, and automated CI validations are 100% complete.
- Auto-merge is active on PR #429928; once a Microsoft/community moderator issues the review approval, the PR will merge automatically into `microsoft/winget-pkgs:master`.
- In accordance with the reporting contract, publication is not claimed prematurely. The package will become installable via `winget install r2cuerdame.CodeSampleX` once the upstream merge and indexing complete.

---

## 6. Post-Merge Verification Runbook

When the upstream manual gate completes (PR #429928 merged):
1. **Search check:**
   ```powershell
   winget search r2cuerdame.CodeSampleX
   ```
2. **Install check:**
   ```powershell
   winget install r2cuerdame.CodeSampleX
   ```
3. **Execution check:**
   ```powershell
   csx version
   ```
4. **Documentation update:**
   Update `README.md` and `docs/distribution.md` to list `winget install r2cuerdame.CodeSampleX` as a standard Windows installation option alongside `install.ps1`.
