# Read-only check for #689: lists Defender detections (event 1116) of Go test
# artifacts since -Since. Exit 0 = none, 1 = test artifacts were detected,
# 2 = the Defender log could not be read (never reported as clean).
# No Defender setting is read or changed.
param([datetime]$Since = (Get-Date).AddDays(-1))

$ErrorActionPreference = 'Stop'
$artifact = '\\go-build\d+\\|\.test\.exe|csx-payload\.exe|\\loopoffice-go\\|\\csx\\gotmp\\|\\TestLauncher'
try {
    $events = @(Get-WinEvent -FilterHashtable @{
        LogName = 'Microsoft-Windows-Windows Defender/Operational'; Id = 1116; StartTime = $Since })
} catch {
    if ($_.FullyQualifiedErrorId -like 'NoMatchingEventsFound*') {
        $events = @()
    } else {
        Write-Output "unreadable: $($_.Exception.Message)"
        exit 2
    }
}
$hits = 0
foreach ($event in $events) {
    $data = @{}
    foreach ($node in ([xml]$event.ToXml()).Event.EventData.Data) { $data[$node.Name] = $node.'#text' }
    $path = [string]$data['Path']
    $kind = if ($path -match $artifact) { $hits++; 'test-artifact' } else { 'other' }
    Write-Output ('{0:o} {1} {2} {3}' -f $event.TimeCreated, $kind, $data['Threat Name'], $path)
}
Write-Output ('since={0:o} detections={1} test-artifact={2}' -f $Since, $events.Count, $hits)
if ($hits -gt 0) { exit 1 }
exit 0
