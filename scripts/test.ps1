#Requires -Version 5.1
<#
.SYNOPSIS
  Run the NeuroForge test suite on Windows (go test).
.DESCRIPTION
  Works from any current directory, finds the repository root itself, requires
  neither `make` nor a C compiler, and never installs dependencies.
.PARAMETER Race
  Run with the race detector (go test -race).
.PARAMETER Verbose
  Pass -v to go test.
.PARAMETER ExtraArgs
  Extra arguments passed verbatim to `go test` (e.g. -run TestName).
.EXAMPLE
  powershell -NoProfile -File scripts/test.ps1 -Race -Verbose
#>
[CmdletBinding()]
param(
	[switch]$Race,
	[switch]$Verbose,
	[Parameter(ValueFromRemainingArguments = $true)]
	[string[]]$ExtraArgs
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '_common.ps1')

Initialize-GoPath
$root = Get-NeuroForgeRoot
Set-Location -LiteralPath $root
Write-Host "neuroforge root : $root"
Write-Host "go version      : $(& go version)"

$goArgs = @('test')
if ($Race) { $goArgs += '-race' }
if ($Verbose) { $goArgs += '-v' }
if ($ExtraArgs) { $goArgs += $ExtraArgs } else { $goArgs += './...' }

Write-Host ""
Write-Host "==> go $($goArgs -join ' ')" -ForegroundColor Cyan
& go @goArgs
exit $LASTEXITCODE
