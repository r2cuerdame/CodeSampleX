package admin

import "strings"

// Worker install commands live here rather than on a public page.
//
// They were the call to action on /contribute, which is gone: a contributor
// turned out to do exactly what any user does -- installing csx is what emits
// observations -- except for running samples to verify them, and the project
// runs those on its own machines. So this is an operator tool, and it belongs
// beside the internal sample workers it pairs with.
func WorkerUnixCMD(base string) string {
	installer := strings.TrimRight(base, "/") + "/install.sh"
	return "CSX_WORKER_ONLY=1 curl -fsSL " + installer + " | sh && " +
		"csx worker start --mode verify --parallel 2 --budget idle"
}

// workerUnixRunCMD reports what is installed and running, then starts the
// worker. The status lines come first because `csx worker start` runs in
// the foreground and never returns while it is working — after it, nothing
// else in the paste would ever print.
func WorkerUnixRunCMD() string {
	return "csx version && csx daemon status\n" +
		"csx worker start --mode verify --parallel 2 --budget idle"
}

// windowsWorkerCMD switches this machine's Docker daemon to Windows
// containers and restarts the worker on it.
//
// A daemon serves one kind of container at a time, so this is the whole
// decision: on Linux containers a Windows machine produces Linux
// receipts, which the network already has plenty of. The command still
// checks the mode it ended in rather than trusting the exit code alone,
// because the switch can fail on a host whose Windows build does not
// match the images.
func WindowsWorkerCMD() string {
	return `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Stop'; ` +
		`docker desktop engine use windows; ` +
		`if ($LASTEXITCODE -ne 0) { throw 'Switching engines failed; Docker Desktop 4.37 or newer is required.' }; ` +
		`$deadline=(Get-Date).AddMinutes(3); ` +
		`do { $os=(docker version --format '{{.Server.Os}}' 2>$null); if ($os -eq 'windows') { break }; Start-Sleep -Seconds 5 } ` +
		`while ((Get-Date) -lt $deadline); ` +
		`if ($os -ne 'windows') { throw \"Docker is still serving $os containers; the switch did not complete.\" }; ` +
		// The ScheduledTasks module has no Restart-ScheduledTask, and a
		// missing cmdlet is a CommandNotFoundException that -ErrorAction
		// cannot suppress. Stop then start, and only if the task is there:
		// switching engines before installing the worker is a fair order.
		`$task='CodeSampleX Contributor Worker'; ` +
		`if (Get-ScheduledTask -TaskName $task -ErrorAction SilentlyContinue) { ` +
		`Stop-ScheduledTask -TaskName $task; Start-ScheduledTask -TaskName $task }; ` +
		`Write-Output \"Docker now serves windows containers; the worker will verify golang and pypi samples on Windows.\""`
}

func WorkerSetupCMD(base string) string {
	installer := powerShellQuote(strings.TrimRight(base, "/") + "/install.ps1")
	return `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Stop'; docker info *> $null; if ($LASTEXITCODE -ne 0) { throw 'Docker must already be installed and running.' }; $env:CSX_WORKER_ONLY='1'; try { irm ` + installer + ` | iex } finally { Remove-Item Env:\CSX_WORKER_ONLY -ErrorAction SilentlyContinue }; $csx=Join-Path $env:LOCALAPPDATA 'csx\csx.exe'; if (-not (Test-Path -LiteralPath $csx)) { throw 'csx was not installed at the expected path.' }; $action=New-ScheduledTaskAction -Execute $csx -Argument 'worker start --mode verify --parallel 2 --budget idle'; $trigger=New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME; $settings=New-ScheduledTaskSettingsSet -Hidden -MultipleInstances IgnoreNew -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero); Register-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' -Action $action -Trigger $trigger -Settings $settings -Description 'Docker-isolated CodeSampleX verification worker' -Force; Start-ScheduledTask -TaskName 'CodeSampleX Contributor Worker'; Get-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' | Select-Object TaskName,State; & $csx version; & $csx daemon status"`
}

func WorkerRunCMD() string {
	return `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Stop'; docker info *> $null; if ($LASTEXITCODE -ne 0) { throw 'Docker must already be installed and running.' }; $csx=Join-Path $env:LOCALAPPDATA 'csx\csx.exe'; if (-not (Test-Path -LiteralPath $csx)) { throw 'csx is not installed at the expected path.' }; Get-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' -ErrorAction Stop | Out-Null; Start-ScheduledTask -TaskName 'CodeSampleX Contributor Worker'; Start-Sleep -Seconds 2; Get-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' | Select-Object TaskName,State; & $csx version; & $csx daemon status"`
}

func powerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// workerInstallBase is where the installers are published. The admin page is
// reached over whatever host the operator typed, which may be an IP or a
// tunnel, so the command must not be built from the request.
const workerInstallBase = "https://codesamplex.dev"
