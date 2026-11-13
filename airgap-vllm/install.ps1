<#
.SYNOPSIS
  Install Claude Code, the vLLM bridge and the desktop app on an air-gapped
  Windows machine.
.DESCRIPTION
  Verifies SHA256SUMS locally, checks the Authenticode signatures on claude.exe
  and the bundled WebView2 runtime, installs everything to
  %USERPROFILE%\.local\bin, adds that directory to the user PATH, and creates a
  "Claude vLLM Desktop" Start Menu shortcut. Requires no network and no
  administrator rights.
.PARAMETER InstallDir
  Where to install claude.exe, claude-vllm.exe, claude-vllm-desktop.exe and the
  webview2 runtime folder. Defaults to %USERPROFILE%\.local\bin.
.PARAMETER NoShortcut
  Do not create the Start Menu shortcut for the desktop app.
.PARAMETER SkipSignatureCheck
  Skip the Authenticode checks on claude.exe and the WebView2 runtime. Use this
  if your organization's policy blocks WinVerifyTrust's revocation check
  outright rather than letting it time out and continue (see the note printed
  by Assert-Signature below).
  The SHA256 check in Assert-Checksums still runs either way.
.PARAMETER Uninstall
  Remove what this script installed: the three executables, the webview2
  folder and the Start Menu shortcut. Settings in %USERPROFILE%\.claude-vllm
  and Claude Code's data in %USERPROFILE%\.claude are kept, and so is the PATH
  entry, since other tools may install to the same directory.
#>
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:USERPROFILE ".local\bin"),
    [switch]$SkipSignatureCheck,
    [switch]$NoShortcut,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"
$source = Split-Path -Parent $MyInvocation.MyCommand.Path

if ($Uninstall) {
    if (Get-Process claude-vllm-desktop -ErrorAction SilentlyContinue) {
        throw "Claude vLLM Desktop is running. Close it, then re-run install.ps1 -Uninstall."
    }
    Write-Host "==> Uninstalling from $InstallDir"
    foreach ($exe in @("claude.exe", "claude-vllm.exe", "claude-vllm-desktop.exe")) {
        $path = Join-Path $InstallDir $exe
        if (Test-Path $path) { Remove-Item -Force $path; Write-Host "  removed $exe" }
    }
    $webview2Target = Join-Path $InstallDir "webview2"
    if (Test-Path $webview2Target) { Remove-Item -Recurse -Force $webview2Target; Write-Host "  removed webview2 runtime" }
    $lnk = Join-Path ([Environment]::GetFolderPath("Programs")) "Claude vLLM Desktop.lnk"
    if (Test-Path $lnk) { Remove-Item -Force $lnk; Write-Host "  removed Start Menu shortcut" }
    Write-Host ""
    Write-Host "Uninstalled." -ForegroundColor Green
    Write-Host "Kept: settings in $env:USERPROFILE\.claude-vllm, Claude Code's data in $env:USERPROFILE\.claude,"
    Write-Host "and the $InstallDir entry on your user PATH. Remove them by hand if you no longer need them."
    return
}

function Assert-Checksums {
    $manifest = Join-Path $source "SHA256SUMS"
    if (-not (Test-Path $manifest)) { throw "SHA256SUMS not found next to install.ps1" }

    foreach ($line in Get-Content $manifest) {
        if ($line -notmatch '^\s*([0-9a-fA-F]{64})\s+\*?(.+?)\s*$') { continue }
        $expected = $Matches[1].ToLower()
        $file     = Join-Path $source $Matches[2]
        if (-not (Test-Path $file)) { throw "listed in SHA256SUMS but missing: $($Matches[2])" }
        $actual = (Get-FileHash $file -Algorithm SHA256).Hash.ToLower()
        if ($actual -ne $expected) {
            throw "checksum mismatch for $($Matches[2])`n  expected $expected`n  actual   $actual`nRe-transfer the bundle; do not proceed with a mismatched file."
        }
        Write-Host "  ok  $($Matches[2])"
    }
}

function Assert-Signature {
    param([string]$Path)
    # Get-AuthenticodeSignature can attempt an online revocation (CRL) check as
    # part of validating the certificate chain. On a machine with no network
    # route out, that check cannot succeed; Windows' default behavior is to
    # time out after some delay and still resolve the signature status rather
    # than fail outright, so this can pause for several seconds before
    # printing "ok". If your environment instead hard-fails when revocation
    # checking is unreachable, the status below will not be "Valid" — rerun
    # with -SkipSignatureCheck and rely on the SHA256 check above, which this
    # script already performed and which needs no network under any policy.
    $sig = Get-AuthenticodeSignature $Path
    if ($sig.Status -ne "Valid") {
        throw "Authenticode signature on $Path is $($sig.Status). Re-transfer the bundle, or re-run with -SkipSignatureCheck if your policy blocks offline revocation checking."
    }
    Write-Host "  ok  signed by $($sig.SignerCertificate.Subject)"
}

Write-Host "==> Verifying bundle checksums"
Assert-Checksums

$webview2Source = Join-Path $source "webview2"
$hasWebView2 = Test-Path (Join-Path $webview2Source "msedgewebview2.exe")
$hasDesktop = Test-Path (Join-Path $source "claude-vllm-desktop.exe")

if (-not $SkipSignatureCheck) {
    Write-Host "==> Verifying the Claude Code code signature"
    Assert-Signature (Join-Path $source "claude.exe")
    if ($hasWebView2) {
        Write-Host "==> Verifying the WebView2 runtime code signature"
        Assert-Signature (Join-Path $webview2Source "msedgewebview2.exe")
    }
} else {
    Write-Host "==> Skipping Authenticode signature checks (-SkipSignatureCheck). SHA256 was still verified above."
}

if ($hasDesktop -and (Get-Process claude-vllm-desktop -ErrorAction SilentlyContinue)) {
    throw "Claude vLLM Desktop is running. Close it, then re-run install.ps1 (its files are in use)."
}

Write-Host "==> Installing to $InstallDir"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
foreach ($exe in @("claude.exe", "claude-vllm.exe", "claude-vllm-desktop.exe")) {
    $from = Join-Path $source $exe
    if (-not (Test-Path $from)) { continue }
    Copy-Item $from (Join-Path $InstallDir $exe) -Force
    Write-Host "  installed $exe"
}

if ($hasWebView2) {
    # Replace rather than merge: a runtime left over from an older bundle would
    # otherwise mix files from two WebView2 versions.
    $webview2Target = Join-Path $InstallDir "webview2"
    if (Test-Path $webview2Target) { Remove-Item -Recurse -Force $webview2Target }
    Copy-Item -Recurse $webview2Source $webview2Target
    Write-Host "  installed webview2 runtime"
} elseif ($hasDesktop) {
    Write-Warning "This bundle has no WebView2 runtime. The desktop app will only start if WebView2 is already installed on this machine; the terminal client is unaffected."
}

if ($hasDesktop -and -not $NoShortcut) {
    $programs = [Environment]::GetFolderPath("Programs")
    $lnk = Join-Path $programs "Claude vLLM Desktop.lnk"
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($lnk)
    $shortcut.TargetPath = Join-Path $InstallDir "claude-vllm-desktop.exe"
    $shortcut.WorkingDirectory = $InstallDir
    $shortcut.Description = "Claude Code on your self-hosted vLLM"
    $shortcut.Save()
    Write-Host "  Start Menu shortcut: $lnk"
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$existingEntries = @()
if ($userPath) { $existingEntries = $userPath.Split(";") | Where-Object { $_ -ne "" } }
$alreadyPresent = $existingEntries | Where-Object {
    $_.TrimEnd("\") -ieq $InstallDir.TrimEnd("\")
}
if (-not $alreadyPresent) {
    $newPath = ($existingEntries + $InstallDir) -join ";"
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Write-Host "  added $InstallDir to your user PATH (restart your terminal)"
} else {
    Write-Host "  $InstallDir already on PATH"
}

# This variable forces Cloud-gateway sign-in and breaks every request. The
# bridge's own launcher strips it from the child claude.exe process, but a
# wrapper-level export left here can still confuse other tooling that reads
# the environment directly, so flag it at install time too.
if ($env:CLAUDE_CODE_USE_GATEWAY) {
    Write-Warning "CLAUDE_CODE_USE_GATEWAY is set in your environment. Unset it: it forces Cloud sign-in and fails every request."
}

Write-Host ""
Write-Host "Installed. Next steps:" -ForegroundColor Green
Write-Host "  1. Open a new terminal"
Write-Host "  2. claude-vllm setup     # point at your vLLM server and pick a model"
Write-Host "  3. claude-vllm doctor    # verify connectivity and tool calling"
Write-Host "  4. claude-vllm           # start Claude Code in the terminal"
if ($hasDesktop) {
    Write-Host "  or open 'Claude vLLM Desktop' from the Start Menu. It opens its Settings"
    Write-Host "  screen on first run if step 2 was skipped, and runs the same doctor there."
}
