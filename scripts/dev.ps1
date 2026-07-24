#Requires -Version 5.1
<#
.SYNOPSIS
  Build the forge binary on Windows with version metadata (like `make build`)
  and optionally run it.
.DESCRIPTION
  Mirrors the POSIX Makefile build: injects version/commit/date via -ldflags,
  producing ./forge.exe. Works from any current directory, finds the repository
  root itself, requires neither `make` nor a C compiler, and never installs
  dependencies.
.PARAMETER Run
  After building, execute ./forge.exe with the remaining Args.
.PARAMETER Args
  When -Run is used, arguments passed to forge.exe (e.g. 'version', 'doctor').
.EXAMPLE
  powershell -NoProfile -File scripts/dev.ps1
.EXAMPLE
  powershell -NoProfile -File scripts/dev.ps1 -Run version
#>
[CmdletBinding()]
param(
	[switch]$Run,
	[Parameter(ValueFromRemainingArguments = $true)]
	[string[]]$Args
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '_common.ps1')

Initialize-GoPath
$root = Get-NeuroForgeRoot
Set-Location -LiteralPath $root
Write-Host "neuroforge root : $root"
Write-Host "go version      : $(& go version)"

# Version metadata, mirroring the Makefile (fall back gracefully when git/n/a).
$version = '0.0.0-dev'
try { $version = (git describe --tags --always --dirty 2>$null).Trim() } catch {}
if (-not $version) { $version = '0.0.0-dev' }
$commit = 'none'
try { $commit = (git rev-parse --short HEAD 2>$null).Trim() } catch {}
if (-not $commit) { $commit = 'none' }
$date = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')

$ldflags = "-X 'neuroforge/internal/version.Version=$version' " +
"-X 'neuroforge/internal/version.Commit=$commit' " +
"-X 'neuroforge/internal/version.Date=$date'"

$out = Join-Path $root 'forge.exe'
Invoke-BuildStage 'go build (forge.exe)' {
	go build -ldflags $ldflags -o $out './cmd/forge'
}

Write-Host ""
Write-Host "built: $out" -ForegroundColor Green

if ($Run) {
	Write-Host ""
	Write-Host "==> forge $($Args -join ' ')" -ForegroundColor Cyan
	& $out @Args
	exit $LASTEXITCODE
}
exit 0
