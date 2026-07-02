#Requires -Version 7
<#
.SYNOPSIS
    Removes leftovers from Codeshot's retired terminal protocol-handler design (Windows).

.DESCRIPTION
    Codeshot no longer installs a codeshot:// protocol handler or a Claude Code
    Stop hook — see README.md's "Status" section. If you installed that older
    design (via install/install.ps1, removed from the repo), this script
    undoes it:
      1. Removes the codeshot:// registry key under HKCU
      2. Removes the Stop hook entry from ~/.claude/settings.json
      3. Removes the ~/.codeshot install directory

    Safe to run even if you never installed the old design — each step is a
    no-op when there's nothing to remove.

.EXAMPLE
    ./install/uninstall-legacy.ps1
#>
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $HOME '.codeshot')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

Write-Host 'Removing legacy Codeshot protocol-handler install...' -ForegroundColor Cyan

# Remove protocol handler registry key
$regKey = 'HKCU:\Software\Classes\codeshot'
if (Test-Path $regKey) {
    Remove-Item -Path $regKey -Recurse -Force
    Write-Host '  Removed codeshot:// registry key'
} else {
    Write-Host '  No codeshot:// registry key found (skipped)'
}

# Remove Stop hook from settings.json
$settingsPath = Join-Path $HOME '.claude\settings.json'
if (Test-Path $settingsPath) {
    $settings = Get-Content $settingsPath -Raw | ConvertFrom-Json -AsHashtable
    $stopHooks = $settings.hooks?.Stop
    if ($stopHooks) {
        $filtered = @($stopHooks | Where-Object {
            -not ($_.hooks | Where-Object { $_.command -match 'codeshot' })
        })
        if ($filtered.Count -ne $stopHooks.Count) {
            $settings.hooks.Stop = $filtered
            $settings | ConvertTo-Json -Depth 10 | Set-Content $settingsPath -Encoding UTF8
            Write-Host '  Removed Stop hook from ~/.claude/settings.json'
        } else {
            Write-Host '  No matching Stop hook found (skipped)'
        }
    } else {
        Write-Host '  No Stop hooks configured (skipped)'
    }
} else {
    Write-Host '  No ~/.claude/settings.json found (skipped)'
}

# Remove install directory
if (Test-Path $InstallDir) {
    Remove-Item -Path $InstallDir -Recurse -Force
    Write-Host "  Removed $InstallDir"
} else {
    Write-Host "  $InstallDir not found (skipped)"
}

Write-Host 'Done.' -ForegroundColor Green
