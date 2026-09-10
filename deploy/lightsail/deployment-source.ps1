# Resolve and prove the two independent immutable identities before remote work.
function Resolve-CSXDeploymentSource {
    param([string]$SourceRepoPath, [string]$ExpectedRevision, [string]$OperationalRevision)
    $controlRepo = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
    $controlSha = (& git -C $controlRepo rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $controlSha -notmatch '^[0-9a-f]{40}$') { throw "could not identify operational source" }
    if ($OperationalRevision -eq "") { $OperationalRevision = $controlSha }
    if ($OperationalRevision -notmatch '^[0-9a-f]{40}$' -or $controlSha -ne $OperationalRevision) {
        throw "operational checkout does not match the immutable workflow revision"
    }
    $controlDirty = @(& git -C $controlRepo status --porcelain --untracked-files=all)
    if ($LASTEXITCODE -ne 0 -or $controlDirty.Count -ne 0) { throw "operational checkout must be clean" }
    if ($SourceRepoPath -eq "") { $SourceRepoPath = $controlRepo }
    $sourceRepo = (Resolve-Path -LiteralPath $SourceRepoPath).Path
    # Git expands Windows 8.3/junction aliases while Resolve-Path can preserve
    # their spelling. Ask Git about the cwd's position instead of comparing
    # two path strings that can name the same directory differently.
    $insideWorktree = @(& git -C $sourceRepo rev-parse --is-inside-work-tree) -join ""
    if ($LASTEXITCODE -ne 0 -or $insideWorktree -cne "true") {
        throw "payload source must be a repository root"
    }
    $prefix = @(& git -C $sourceRepo rev-parse --show-prefix) -join ""
    if ($LASTEXITCODE -ne 0 -or $prefix -cne "") {
        throw "payload source must be a repository root"
    }
    $sourceSha = (& git -C $sourceRepo rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $ExpectedRevision -notmatch '^[0-9a-f]{40}$' -or $sourceSha -ne $ExpectedRevision) {
        throw "payload checkout does not match the immutable target revision"
    }
    $sourceDirty = @(& git -C $sourceRepo status --porcelain --untracked-files=all)
    if ($LASTEXITCODE -ne 0 -or $sourceDirty.Count -ne 0) { throw "payload checkout must be clean" }
    [pscustomobject]@{ Repository = $sourceRepo; Revision = $sourceSha; OperationalRevision = $controlSha }
}
