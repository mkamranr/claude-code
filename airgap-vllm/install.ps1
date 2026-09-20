<#
.SYNOPSIS
  Install Claude Code and the vLLM bridge on an air-gapped Windows machine.
.DESCRIPTION
  Verifies SHA256SUMS locally, checks the Authenticode signature on claude.exe,
  installs both binaries to %USERPROFILE%\.local\bin, and adds that directory to
  the user PATH. Requires no network and no administrator rights.
.PARAMETER InstallDir
  Where to install claude.exe and claude-vllm.exe. Defaults to
  %USERPROFILE%\.local\bin.
.PARAMETER SkipSignatureCheck
  Skip the Authenticode check on claude.exe. Use this if your organization's
  policy blocks WinVerifyTrust's revocation check outright rather than letting
  it time out and continue (see the note printed by Assert-Signature below).
  The SHA256 check in Assert-Checksums still runs either way.
#>
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:USERPROFILE ".local\bin"),
    [switch]$SkipSignatureCheck
)

$ErrorActionPreference = "Stop"
$source = Split-Path -Parent $MyInvocation.MyCommand.Path

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

if (-not $SkipSignatureCheck) {
    Write-Host "==> Verifying the Claude Code code signature"
    Assert-Signature (Join-Path $source "claude.exe")
} else {
    Write-Host "==> Skipping Authenticode signature check (-SkipSignatureCheck). SHA256 was still verified above."
}

Write-Host "==> Installing to $InstallDir"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
foreach ($exe in @("claude.exe", "claude-vllm.exe")) {
    Copy-Item (Join-Path $source $exe) (Join-Path $InstallDir $exe) -Force
    Write-Host "  installed $exe"
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
Write-Host "  4. claude-vllm           # start Claude Code"
