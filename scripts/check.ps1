#Requires -Version 5.1
<#
.SYNOPSIS
  Windows first-class equivalent of `make check` (gofmt check + go vet + tests).
.DESCRIPTION
  This is the CI gate for Windows. It mirrors the POSIX Makefile's `check`
  target: fmt-check + vet + test. It works from any current directory, finds the
  repository root itself, requires neither `make` nor a C compiler, never
  installs dependencies, and exits nonzero if any stage fails.
.PARAMETER Race
  Run the race detector (go test -race). The race detector needs cgo enabled on
  some configurations; the script does not force CGO_ENABLED.
.EXAMPLE
  powershell -NoProfile -File scripts/check.ps1
.EXAMPLE
  powershell -NoProfile -File scripts/check.ps1 -Race
#>
[CmdletBinding()]
param(
	[switch]$Race
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '_common.ps1')

Initialize-GoPath
$root = Get-NeuroForgeRoot
Set-Location -LiteralPath $root
Write-Host "neuroforge root : $root"
Write-Host "go version      : $(& go version)"

try {
	Invoke-BuildStage 'gofmt check' {
		$unformatted = (& gofmt -l .) | Where-Object { $_ }
		if ($unformatted) {
			Write-Host 'gofmt would reformat the following files:' -ForegroundColor Yellow
			$unformatted | ForEach-Object { Write-Host "  $_" }
			throw 'gofmt check failed (run: gofmt -w .)'
		}
		Write-Host 'gofmt: clean'
	}

	Invoke-BuildStage 'go vet' { go vet ./... }

	Invoke-BuildStage 'go test' {
		if ($Race) { go test -race ./... } else { go test ./... }
	}
} catch {
	Write-Host ""
	Write-Host "CHECK FAILED: $($_.Exception.Message)" -ForegroundColor Red
	exit 1
}

Write-Host ""
Write-Host 'check: OK' -ForegroundColor Green
exit 0
