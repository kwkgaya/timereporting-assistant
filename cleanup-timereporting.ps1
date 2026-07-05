# ─── Timereporting Assistant — Full cleanup before reinstall ───────────────
# Removes: old config files, tray state, keychain credentials, autostart key.
# Safe to run multiple times.

Write-Host "
=== Timereporting Assistant Cleanup ===" -ForegroundColor Cyan

# 1. Config in AppData (v0.7.3+ default location)
$appData = Join-Path $env:LOCALAPPDATA "timereporting-assistant"
if (Test-Path $appData) {
    Remove-Item $appData -Recurse -Force
    Write-Host "Removed: $appData" -ForegroundColor Green
} else {
    Write-Host "Not found: $appData" -ForegroundColor Gray
}

# 2. Old config.json next to the installed binary (v0.3.0 – v0.7.2)
$installDir = Join-Path $env:LOCALAPPDATA "Programs\TimereportingAssistant"
$oldConfig  = Join-Path $installDir "config.json"
if (Test-Path $oldConfig) {
    Remove-Item $oldConfig -Force
    Write-Host "Removed: $oldConfig" -ForegroundColor Green
} else {
    Write-Host "Not found: $oldConfig" -ForegroundColor Gray
}

# 3. Windows Credential Manager entries
foreach ($name in @("timereporting-assistant/jira", "timereporting-assistant/github")) {
    try {
        $cred = [void][Windows.Security.Credentials.PasswordVault, Windows.Security.Credentials, ContentType=WindowsRuntime]
        # Use cmdkey which works without extra assemblies
        cmdkey /delete:$name 2>$null | Out-Null
        Write-Host "Removed credential: $name" -ForegroundColor Green
    } catch {}
    cmdkey /delete:$name 2>$null | Out-Null
}

# 4. Autostart registry key (per-user Run)
$runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
if (Get-ItemProperty -Path $runKey -Name "TimereportingAssistant" -ErrorAction SilentlyContinue) {
    Remove-ItemProperty -Path $runKey -Name "TimereportingAssistant" -ErrorAction SilentlyContinue
    Write-Host "Removed autostart registry key" -ForegroundColor Green
} else {
    Write-Host "Not found: autostart registry key" -ForegroundColor Gray
}

# 5. Kill any still-running processes
foreach ($proc in @("tray", "timeporting", "mockjira")) {
    $ps = Get-Process -Name $proc -ErrorAction SilentlyContinue
    if ($ps) {
        Stop-Process -Name $proc -Force -ErrorAction SilentlyContinue
        Write-Host "Stopped process: $proc.exe" -ForegroundColor Green
    }
}

Write-Host "
=== Done. Safe to reinstall. ===" -ForegroundColor Cyan
