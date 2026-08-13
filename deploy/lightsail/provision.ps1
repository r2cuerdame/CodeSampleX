# Provision the CodeSampleX production host on AWS Lightsail.
# User-confirmed plan: 2 vCPU / 2GB RAM / 60GB SSD / 3TB transfer ($12/mo).
# Usage: .\provision.ps1 [-Name csx-prod-1] [-Region <aws region>]
param(
    [string]$Name = "csx-prod-1",
    [string]$Region = $(aws configure get region),
    [string]$Blueprint = "ubuntu_24_04"
)
$ErrorActionPreference = "Stop"
if (-not $Region) { throw "No AWS region configured; pass -Region" }

# Pick the current-generation Linux bundle with exactly 2GB RAM ($12 plan).
$bundles = aws lightsail get-bundles --region $Region --query "bundles[?supportedPlatforms[0]=='LINUX_UNIX' && ramSizeInGb==``2.0``].{id:bundleId,price:price}" --output json | ConvertFrom-Json
if (-not $bundles) { throw "No 2GB Linux bundle found in $Region" }
$bundle = $bundles[0].id
Write-Output "Using bundle $bundle (`$$($bundles[0].price)/mo) in $Region"

$az = (aws lightsail get-regions --include-availability-zones --region $Region --query "regions[?name=='$Region'].availabilityZones[0].zoneName" --output text)
if (-not $az -or $az -eq "None") { $az = "${Region}a" }

aws lightsail create-instances --region $Region --instance-names $Name `
    --availability-zone $az --blueprint-id $Blueprint --bundle-id $bundle `
    --user-data (Get-Content -Raw (Join-Path $PSScriptRoot "userdata.sh"))

Write-Output "Waiting for instance to run..."
do { Start-Sleep 10; $state = aws lightsail get-instance --region $Region --instance-name $Name --query "instance.state.name" --output text } while ($state -ne "running")

# Static IP + firewall: 22 (SSH), 80/443 (Caddy).
aws lightsail allocate-static-ip --region $Region --static-ip-name "$Name-ip" 2>$null
aws lightsail attach-static-ip --region $Region --static-ip-name "$Name-ip" --instance-name $Name
aws lightsail put-instance-public-ports --region $Region --instance-name $Name --port-infos `
    '{"fromPort":22,"toPort":22,"protocol":"tcp"}' `
    '{"fromPort":80,"toPort":80,"protocol":"tcp"}' `
    '{"fromPort":443,"toPort":443,"protocol":"tcp"}'

$ip = aws lightsail get-static-ip --region $Region --static-ip-name "$Name-ip" --query "staticIp.ipAddress" --output text
Write-Output "Instance $Name running at $ip"
Write-Output "Download the default key from the Lightsail console or use your own key pair, then run: .\deploy.ps1 -Ip $ip -KeyPath <pem>"
