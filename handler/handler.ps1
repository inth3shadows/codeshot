#Requires -Version 7
<#
.SYNOPSIS
    Codeshot protocol handler for Windows.

.DESCRIPTION
    Registered as the handler for codeshot:// URIs.
    Decodes the code from the URL, writes a launcher script to a temp file,
    then opens a new Windows Terminal tab running that script.

    The launcher shows the code for review, waits for Enter, then executes.
    Ctrl+C cancels at any point.

.PARAMETER Uri
    The full codeshot:// URI passed by Windows when the link is clicked.
#>
param([string]$Uri)

Add-Type -AssemblyName System.Web

function Decode-Uri([string]$s) {
    [System.Web.HttpUtility]::UrlDecode($s)
}

function Parse-CodeshotUri([string]$Uri) {
    $u      = [Uri]$Uri
    $query  = [System.Web.HttpUtility]::ParseQueryString($u.Query)
    $host_  = $u.Host   # 'run' or 'file'
    $cwd    = if ($query['cwd'])  { Decode-Uri $query['cwd']  } else { $env:USERPROFILE }

    if ($host_ -eq 'file') {
        $filePath = Decode-Uri $query['path']
        $code = Get-Content -LiteralPath $filePath -Raw -ErrorAction Stop
        return @{ Code = $code; Cwd = $cwd; TmpFile = $filePath }
    }
    elseif ($host_ -eq 'run') {
        $code = Decode-Uri $query['code']
        return @{ Code = $code; Cwd = $cwd; TmpFile = $null }
    }
    else {
        Write-Error "Unknown codeshot URI host: $host_"
        exit 1
    }
}

function Write-CodeFile([string]$Code) {
    $tmp = Join-Path $env:TEMP "codeshot-code-$([System.Guid]::NewGuid().ToString('N').Substring(0,8)).ps1"
    Set-Content -LiteralPath $tmp -Value $Code -Encoding UTF8 -NoNewline
    return $tmp
}

function Write-Launcher([string]$CodeFile, [string]$Cwd) {
    # Escape single-quoted strings by doubling internal quotes
    $escapedCwd      = $Cwd.Replace("'", "''")
    $escapedCodeFile = $CodeFile.Replace("'", "''")

    $launcher = @"
Set-Location '$escapedCwd' -ErrorAction SilentlyContinue
`$codeFile = '$escapedCodeFile'
`$code = Get-Content -LiteralPath `$codeFile -Raw

Write-Host ''
Write-Host ' Codeshot ' -ForegroundColor Black -BackgroundColor Cyan -NoNewline
Write-Host ' Review then press Enter to execute  (Ctrl+C to cancel)' -ForegroundColor Cyan
Write-Host ''
Write-Host `$code -ForegroundColor Yellow
Write-Host ''
Write-Host ('-' * 52) -ForegroundColor DarkGray

`$null = `$Host.UI.ReadLine()

Invoke-Expression `$code

Remove-Item -LiteralPath `$codeFile -ErrorAction SilentlyContinue
"@

    $tmp = Join-Path $env:TEMP "codeshot-launcher-$([System.Guid]::NewGuid().ToString('N').Substring(0,8)).ps1"
    Set-Content -LiteralPath $tmp -Value $launcher -Encoding UTF8
    return $tmp
}

function Emit-RunEchoSignal([string]$Cwd, [string]$CodePreview) {
    $faultsLog = Join-Path $Cwd '.ai\faults.jsonl'
    if (-not (Test-Path $faultsLog)) { return }
    try {
        $record = [pscustomobject]@{
            session_id = 'codeshot'
            signal     = 'USER_EXEC'
            ts         = (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssZ')
            value      = 1
            context    = "Manual execution: $($CodePreview.Substring(0, [Math]::Min(80, $CodePreview.Length)))"
        } | ConvertTo-Json -Compress
        Add-Content -LiteralPath $faultsLog -Value $record -Encoding UTF8
    } catch { <# never crash over optional integration #> }
}

# ── Main ──────────────────────────────────────────────────────────────────

$parsed  = Parse-CodeshotUri $Uri
$code    = $parsed.Code
$cwd     = $parsed.Cwd

if (-not $code -or -not $code.Trim()) { exit 0 }

$codeFile    = Write-CodeFile $code
$launcherFile = Write-Launcher $codeFile $cwd

# Open new Windows Terminal tab — no need for admin, wt is in PATH on Win 11
Start-Process wt -ArgumentList "new-tab", "--", "pwsh", "-NoExit", "-File", $launcherFile

Emit-RunEchoSignal $cwd ($code.Split("`n")[0])
