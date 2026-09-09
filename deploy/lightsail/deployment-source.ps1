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
    $top = (& git -C $sourceRepo rev-parse --show-toplevel).Trim()
    if ($LASTEXITCODE -ne 0 -or $top -eq "") { throw "payload source must be a repository root" }
    # Ask Git whether -C is the worktree root. Comparing path strings is not
    # reliable on Windows, where Git can return an equivalent 8.3 short path.
    $prefix = (& git -C $sourceRepo rev-parse --show-prefix).Trim()
    if ($LASTEXITCODE -ne 0 -or $prefix -ne "") {
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
