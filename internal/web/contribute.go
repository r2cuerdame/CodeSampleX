package web

import (
	"net/http"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// This file is /contribute: what the network accepts from strangers, and
// what it does not.
//
// It exists because the publish endpoint refuses anonymous uploads and
// points here. A gate that says only "forbidden" teaches nothing and reads
// as a closed project; the same gate beside a page naming three open
// channels reads as a policy. The refusal message and this page are one
// thing in two places, and they must not drift — publishgate_test.go pins
// the message, and TestContributePageAnswersTheRefusal pins that this page
// answers it.
type contributePage struct {
	basePage
	// SourceURL is the repository. Everything on this page is checkable
	// there, which is the only reason a stranger should believe it.
	SourceURL string
	IssuesURL string
	// Setup and run are separate because installing a persistent worker is a
	// one-time operation while checking or starting it is routine.
	WorkerSetupPrompt string
	WorkerRunPrompt   string
	WorkerSetupCMD    string
	WorkerRunCMD      string
	// WindowsSwitchCMD moves an already-installed worker onto Windows
	// containers. It is separate from setup because it is a choice about
	// WHICH platform this machine contributes evidence for, not another
	// way to install.
	WindowsSwitchCMD string
}

func (s *site) contribute(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	b := s.page(r, lang, i18n.T(lang, "contribute.title")+" — CodeSampleX",
		i18n.T(lang, "meta.contribute"))
	s.render(w, "contribute", http.StatusOK, contributePage{
		basePage:          b,
		SourceURL:         "https://github.com/r2cuerdame/CodeSampleX",
		IssuesURL:         "https://github.com/r2cuerdame/CodeSampleX/issues/new",
		WorkerSetupPrompt: workerSetupPrompt(lang, s.base(r)),
		WorkerRunPrompt:   workerRunPrompt(lang),
		WorkerSetupCMD:    workerSetupCMD(s.base(r)),
		WorkerRunCMD:      workerRunCMD(),
		WindowsSwitchCMD:  windowsWorkerCMD(),
	})
}

func workerSetupPrompt(lang, base string) string {
	return i18n.T(lang, "landing.worker_setup_prompt", base, base)
}

func workerRunPrompt(lang string) string {
	return i18n.T(lang, "landing.worker_run_prompt")
}

// windowsWorkerCMD switches this machine's Docker daemon to Windows
// containers and restarts the worker on it.
//
// A daemon serves one kind of container at a time, so this is the whole
// decision: on Linux containers a Windows machine produces Linux
// receipts, which the network already has plenty of. The command checks
// the mode it ended in rather than assuming the switch worked —
// DockerCli.exe is only present with Docker Desktop, and switching can
// fail on a host whose Windows build does not match the images.
func windowsWorkerCMD() string {
	return `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Stop'; ` +
		`$cli=Join-Path $env:ProgramFiles 'Docker\Docker\DockerCli.exe'; ` +
		`if (-not (Test-Path -LiteralPath $cli)) { throw 'Docker Desktop is required to switch container modes.' }; ` +
		`& $cli -SwitchWindowsEngine; ` +
		`$deadline=(Get-Date).AddMinutes(3); ` +
		`do { Start-Sleep -Seconds 5; $os=(docker version --format '{{.Server.Os}}' 2>$null) } ` +
		`while ($os -ne 'windows' -and (Get-Date) -lt $deadline); ` +
		`if ($os -ne 'windows') { throw \"Docker is still serving $os containers; the switch did not complete.\" }; ` +
		`Restart-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' -ErrorAction SilentlyContinue; ` +
		`Write-Output \"Docker now serves windows containers; the worker will verify golang and pypi samples on Windows.\""`
}

func workerSetupCMD(base string) string {
	installer := powerShellQuote(strings.TrimRight(base, "/") + "/install.ps1")
	return `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Stop'; docker info *> $null; if ($LASTEXITCODE -ne 0) { throw 'Docker must already be installed and running.' }; $env:CSX_WORKER_ONLY='1'; try { irm ` + installer + ` | iex } finally { Remove-Item Env:\CSX_WORKER_ONLY -ErrorAction SilentlyContinue }; $csx=Join-Path $env:LOCALAPPDATA 'csx\csx.exe'; if (-not (Test-Path -LiteralPath $csx)) { throw 'csx was not installed at the expected path.' }; $action=New-ScheduledTaskAction -Execute $csx -Argument 'worker start --mode verify --parallel 2 --budget idle'; $trigger=New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME; $settings=New-ScheduledTaskSettingsSet -Hidden -MultipleInstances IgnoreNew -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero); Register-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' -Action $action -Trigger $trigger -Settings $settings -Description 'Docker-isolated CodeSampleX verification worker' -Force; Start-ScheduledTask -TaskName 'CodeSampleX Contributor Worker'; Get-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' | Select-Object TaskName,State; & $csx version; & $csx daemon status"`
}

func workerRunCMD() string {
	return `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Stop'; docker info *> $null; if ($LASTEXITCODE -ne 0) { throw 'Docker must already be installed and running.' }; $csx=Join-Path $env:LOCALAPPDATA 'csx\csx.exe'; if (-not (Test-Path -LiteralPath $csx)) { throw 'csx is not installed at the expected path.' }; Get-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' -ErrorAction Stop | Out-Null; Start-ScheduledTask -TaskName 'CodeSampleX Contributor Worker'; Start-Sleep -Seconds 2; Get-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' | Select-Object TaskName,State; & $csx version; & $csx daemon status"`
}

func powerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
