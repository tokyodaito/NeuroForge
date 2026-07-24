#Requires -Version 5.1
<#
.SYNOPSIS
  Diagnose the Windows development environment for NeuroForge.
.DESCRIPTION
  Reports the presence/absence of every tool NeuroForge needs on Windows and
  highlights missing prerequisites. This checks the DEVELOPMENT environment
  (toolchain, git, paths) -- for the daemon runtime health check use
  `forge doctor`. It never modifies the system and never installs anything.
  Exit code is 0 when all mandatory tools are present, 1 otherwise.
.EXAMPLE
  powershell -NoProfile -File scripts/doctor.ps1
#>
[CmdletBinding()]
param()

. (Join-Path $PSScriptRoot '_common.ps1')

function Section($t) { Write-Host ""; Write-Host "=== $t ===" -ForegroundColor Cyan }
function OK($msg)    { Write-Host "  [OK]   $msg" -ForegroundColor Green }
function Warn($msg)  { Write-Host "  [WARN] $msg" -ForegroundColor Yellow }
function Miss($msg)  { Write-Host "  [MISS] $msg" -ForegroundColor Red }

$missing = 0

Section 'Operating system'
$os = (Get-CimInstance Win32_OperatingSystem).Caption
$bld = [System.Environment]::OSVersion.Version.ToString()
Write-Host "  os      : $os (build $bld)"
Write-Host "  arch    : $env:PROCESSOR_ARCHITECTURE"
Write-Host "  shell   : PowerShell $($PSVersionTable.PSVersion.ToString())"
$lp = $null
try { $lp = (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem' -Name LongPathsEnabled -ErrorAction Stop).LongPathsEnabled } catch {}
if ($lp -eq 1) { OK "long paths enabled (recommended)" } else { Warn "long paths not enabled (set LongPathsEnabled=1 to avoid MAX_PATH issues)" }

Section 'Mandatory tools'
# Go
$goOk = $false
try { Initialize-GoPath -ErrorAction Stop; $goOk = $true } catch {}
if ($goOk -and (Get-Command go -ErrorAction SilentlyContinue)) {
	$gv = (& go version) 2>$null
	OK "go: $gv"
	$goroot = (& go env GOROOT) 2>$null
	Write-Host "         GOROOT=$goroot"
	$cgo = (& go env CGO_ENABLED) 2>$null
	Write-Host "         CGO_ENABLED=$cgo (the SQLite driver is pure-Go; a C compiler is NOT required)"
} else {
	Miss "go: not found. Install Go >= 1.23 (https://go.dev/dl/ or 'winget install GoLang.Go')."
	$missing++
}

# Git
if (Get-Command git -ErrorAction SilentlyContinue) {
	OK "git: $((& git --version) 2>$null)"
} else {
	Miss "git: not found. Install Git (e.g. 'winget install Git.Git')."
	$missing++
}

Section 'Optional tools (informational)'
if (Get-Command make -ErrorAction SilentlyContinue) { OK "make present (POSIX scripts usable)" } else { Write-Host "  [info] make not found (use the scripts/ PowerShell scripts instead -- first-class on Windows)" }
if (Get-Command pwsh -ErrorAction SilentlyContinue) { OK "PowerShell 7 (pwsh) present" } else { Warn "PowerShell 7 not found; scripts run on Windows PowerShell 5.1 (fine)" }
foreach ($t in 'gcc', 'cl') {
	if (Get-Command $t -ErrorAction SilentlyContinue) { Write-Host "  [info] $t present (not required)" }
}

Section 'NeuroForge layout'
try {
	$root = Get-NeuroForgeRoot
	OK "repository root: $root"
	Set-Location -LiteralPath $root
} catch {
	Miss $_.Exception.Message
	$missing++
}

# Resolve the runtime home the daemon would use.
$home_dir = $env:NEUROFORGE_HOME
if (-not $home_dir) {
	try { $home_dir = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.neuroforge' } catch { $home_dir = '<UserProfile>\.neuroforge' }
}
Write-Host "  NEUROFORGE_HOME : $home_dir"
Write-Host "  (set NEUROFORGE_HOME to override; e.g. tests use a temp dir)"

if ($goOk -and (Get-Command go -ErrorAction SilentlyContinue) -and (Get-NeuroForgeRoot -ErrorAction SilentlyContinue)) {
	Section 'Module'
	Invoke-BuildStage 'go vet (quick build check)' { go vet ./... } | Out-Null
}

Section 'Result'
if ($missing -eq 0) {
	Write-Host "  All mandatory tools present. Build with: powershell -File scripts/dev.ps1" -ForegroundColor Green
	exit 0
} else {
	Write-Host "  $missing mandatory tool(s) missing." -ForegroundColor Red
	exit 1
}
