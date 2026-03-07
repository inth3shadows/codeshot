#Requires -Version 7
<#
.SYNOPSIS
    Installs Codeshot on Windows.

.DESCRIPTION
    1. Copies hook.js and handler.ps1 to ~/.codeshot/
    2. Registers the codeshot:// protocol handler in HKCU (no admin required)
    3. Adds the Stop hook to ~/.claude/settings.json

    Idempotent — safe to re-run after updates.

.EXAMPLE
    irm https://raw.githubusercontent.com/inth3shadows/codeshot/main/install/install.ps1 | iex

.EXAMPLE
    # From a local clone:
    ./install/install.ps1
#>
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $HOME '.codeshot'),
    [switch]$Uninstall
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ── Resolve source root ────────────────────────────────────────────────────

# When piped via iex the script has no $PSScriptRoot; detect a local clone.
$scriptRoot = if ($PSScriptRoot) { Split-Path $PSScriptRoot -Parent } else { $PWD.Path }
$hookSrc    = Join-Path $scriptRoot 'hook\hook.js'
$handlerSrc = Join-Path $scriptRoot 'handler\handler.ps1'

function Ensure-Dir([string]$Path) {
    if (-not (Test-Path $Path)) { New-Item -ItemType Directory -Path $Path -Force | Out-Null }
}

# ── Uninstall ──────────────────────────────────────────────────────────────

if ($Uninstall) {
    Write-Host 'Uninstalling Codeshot...' -ForegroundColor Cyan

    # Remove protocol handler registry key
    $regKey = 'HKCU:\Software\Classes\codeshot'
    if (Test-Path $regKey) {
        Remove-Item -Path $regKey -Recurse -Force
        Write-Host '  Removed codeshot:// registry key'
    }

    # Remove hook from settings.json
    $settingsPath = Join-Path $HOME '.claude\settings.json'
    if (Test-Path $settingsPath) {
        $settings = Get-Content $settingsPath -Raw | ConvertFrom-Json -AsHashtable
        $stopHooks = $settings.hooks?.Stop
        if ($stopHooks) {
            $settings.hooks.Stop = @($stopHooks | Where-Object {
                $_.hooks -notmatch 'codeshot'
            })
            $settings | ConvertTo-Json -Depth 10 | Set-Content $settingsPath -Encoding UTF8
            Write-Host '  Removed Stop hook from ~/.claude/settings.json'
        }
    }

    # Remove install directory
    if (Test-Path $InstallDir) {
        Remove-Item -Path $InstallDir -Recurse -Force
        Write-Host "  Removed $InstallDir"
    }

    Write-Host 'Done.' -ForegroundColor Green
    exit 0
}

# ── Install ────────────────────────────────────────────────────────────────

Write-Host 'Installing Codeshot...' -ForegroundColor Cyan

# 1. Copy files to ~/.codeshot/
Ensure-Dir $InstallDir
Ensure-Dir (Join-Path $InstallDir 'hook')
Ensure-Dir (Join-Path $InstallDir 'handler')

$hookDst    = Join-Path $InstallDir 'hook\hook.js'
$handlerDst = Join-Path $InstallDir 'handler\handler.ps1'

Copy-Item -LiteralPath $hookSrc    -Destination $hookDst    -Force
Copy-Item -LiteralPath $handlerSrc -Destination $handlerDst -Force
Write-Host "  Copied files to $InstallDir"

# 2. Register codeshot:// protocol handler (HKCU, no admin required)
$regBase = 'HKCU:\Software\Classes\codeshot'
$cmdKey  = "$regBase\shell\open\command"

New-Item -Path $regBase -Force | Out-Null
Set-ItemProperty -Path $regBase -Name '(Default)'    -Value 'URL:Codeshot Protocol'
Set-ItemProperty -Path $regBase -Name 'URL Protocol' -Value ''

New-Item -Path $cmdKey -Force | Out-Null
$handlerCmd = "pwsh -NonInteractive -WindowStyle Hidden -File `"$handlerDst`" `"%1`""
Set-ItemProperty -Path $cmdKey -Name '(Default)' -Value $handlerCmd

Write-Host '  Registered codeshot:// protocol handler'

# 3. Add Stop hook to ~/.claude/settings.json
$claudeDir    = Join-Path $HOME '.claude'
$settingsPath = Join-Path $claudeDir 'settings.json'
Ensure-Dir $claudeDir

$hookCommand = "node `"$hookDst`""

# Load or create settings
$settings = if (Test-Path $settingsPath) {
    Get-Content $settingsPath -Raw | ConvertFrom-Json -AsHashtable
} else {
    @{}
}

# Ensure hooks.Stop exists
if (-not $settings.ContainsKey('hooks'))       { $settings['hooks'] = @{} }
if (-not $settings.hooks.ContainsKey('Stop'))  { $settings.hooks['Stop'] = @() }

# Check if already registered (idempotent)
$alreadyRegistered = $settings.hooks.Stop | Where-Object {
    $_.hooks | Where-Object { $_.command -like '*codeshot*' }
}

if (-not $alreadyRegistered) {
    $hookEntry = @{
        matcher = ''
        hooks   = @(
            @{
                type    = 'command'
                command = $hookCommand
            }
        )
    }
    $settings.hooks.Stop = @($settings.hooks.Stop) + @($hookEntry)
    $settings | ConvertTo-Json -Depth 10 | Set-Content $settingsPath -Encoding UTF8
    Write-Host "  Added Stop hook to $settingsPath"
} else {
    Write-Host '  Stop hook already registered (skipped)'
}

Write-Host ''
Write-Host 'Codeshot installed.' -ForegroundColor Green
Write-Host 'Start a new Claude Code session and click any code block to try it.'
