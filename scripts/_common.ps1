#Requires -Version 5.1
<#
.SYNOPSIS
  Shared helpers for the NeuroForge Windows PowerShell scripts.
.DESCRIPTION
  Dot-sourced by check.ps1 / test.ps1 / dev.ps1 / doctor.ps1. Provides:
    - Get-NeuroForgeRoot : locate the repo root (the directory holding go.mod),
                           regardless of the current working directory.
    - Initialize-GoPath  : make the Go toolchain reachable without ever silently
                           installing it; refreshes PATH from the registry to pick
                           up a just-installed Go, then fails loudly if still gone.
    - Invoke-BuildStage  : print and run a named stage, propagating its exit code.
  These scripts never modify system state and never install dependencies.
#>

# Walk up from the script location (scripts/) to the directory holding go.mod.
function Get-NeuroForgeRoot {
	$start = $PSScriptRoot
	if (-not $start) { $start = (Get-Location).Path }
	$dir = $start
	while ($dir) {
		if (Test-Path -LiteralPath (Join-Path $dir 'go.mod')) { return $dir }
		$parent = Split-Path $dir -Parent
		if (-not $parent -or $parent -eq $dir) { break }
		$dir = $parent
	}
	# Fall back to walking up from the current directory.
	$dir = (Get-Location).Path
	while ($dir) {
		if (Test-Path -LiteralPath (Join-Path $dir 'go.mod')) { return $dir }
		$parent = Split-Path $dir -Parent
		if (-not $parent -or $parent -eq $dir) { break }
		$dir = $parent
	}
	throw "could not locate NeuroForge repository root (no go.mod found upward of '$start')."
}

# Ensure `go` is on PATH. A freshly installed Go (e.g. via winget) updates the
# Machine/User environment but not the current process; we refresh from the
# registry. We never install Go silently here.
function Initialize-GoPath {
	if (Get-Command go -ErrorAction SilentlyContinue) { return }
	$machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
	$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
	if ($machinePath -and $userPath) {
		$env:Path = "$machinePath;$userPath"
	} else {
		$env:Path = "$machinePath$userPath"
	}
	if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
		throw "Go toolchain not found on PATH. Install Go >= 1.23 (https://go.dev/dl/ or 'winget install GoLang.Go') and re-run. NeuroForge never installs it silently."
	}
}

# Run a named build stage, echo a header, and abort the script on nonzero exit.
function Invoke-BuildStage {
	param(
		[Parameter(Mandatory)][string]$Name,
		[Parameter(Mandatory)][scriptblock]$Action
	)
	Write-Host ""
	Write-Host "==> $Name" -ForegroundColor Cyan
	& $Action
	if ($LASTEXITCODE -ne 0) {
		throw "stage '$Name' failed with exit code $LASTEXITCODE."
	}
}
