; Inno Setup script for Timereporting Assistant.
; Per-user install (no admin required). Bundles the main app and, optionally,
; the mock Jira server. Built by CI (see .github/workflows/release.yml).
;
; Compile with:
;   iscc /DAppVersion=v0.1.0 /DDistDir=C:\path\to\dist build\installer\timereporting.iss
; Expects DistDir to contain timeporting.exe and mockjira.exe.

#ifndef AppVersion
  #define AppVersion "dev"
#endif
#ifndef DistDir
  #define DistDir "..\..\dist"
#endif

[Setup]
AppId={{7F3C1E92-4B7A-4E2D-9C1B-TR8REP0RT1NG}}
AppName=Timereporting Assistant
AppVersion={#AppVersion}
AppPublisher=kwkgaya
AppSupportURL=https://github.com/kwkgaya/timereporting-assistant
DefaultDirName={localappdata}\Programs\TimereportingAssistant
DefaultGroupName=Timereporting Assistant
DisableProgramGroupPage=yes
DisableDirPage=yes
; Per-user install => no admin prompt.
PrivilegesRequired=lowest
OutputDir={#DistDir}
OutputBaseFilename=TimereportingAssistant-Setup-{#AppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ArchitecturesInstallIn64BitMode=x64compatible
; Never prompt to close running processes — kill them automatically.
CloseApplications=force
RestartApplications=no

[Files]
Source: "{#DistDir}\timeporting.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#DistDir}\tray.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#DistDir}\config.example.json"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#DistDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\Timereporting Assistant"; Filename: "{app}\timeporting.exe"; Comment: "Review and submit your time reports"
Name: "{group}\Timereporting Tray"; Filename: "{app}\tray.exe"; Comment: "System-tray companion (also starts automatically at login)"
Name: "{group}\Uninstall Timereporting Assistant"; Filename: "{uninstallexe}"
Name: "{userdesktop}\Timereporting Assistant"; Filename: "{app}\timeporting.exe"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; Flags: unchecked
Name: "autostart"; Description: "Start tray app when I log in (recommended)"

[Registry]
; Optional auto-start at login (per-user, no admin).
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; \
  ValueType: string; ValueName: "TimereportingAssistant"; \
  ValueData: """{app}\tray.exe"""; \
  Tasks: autostart; Flags: uninsdeletevalue

[Run]
; Always (re)start the tray companion after install — including SILENT
; auto-updates, so the app comes back automatically after updating.
Filename: "{app}\tray.exe"; \
  Flags: nowait

; Start the review server — it auto-opens the setup wizard in the browser
; on first run (no config yet). Only on interactive installs (skipifsilent),
; so silent auto-updates don't pop a browser window.
Filename: "{app}\timeporting.exe"; \
  Description: "Open configuration wizard now"; \
  Flags: nowait postinstall skipifsilent runhidden

[UninstallRun]
; Silently terminate all app processes before files are deleted.
; This prevents the "application has to be closed" prompt on uninstall.
Filename: "taskkill"; Parameters: "/f /im tray.exe"; \
  Flags: runhidden skipifdoesntexist
Filename: "taskkill"; Parameters: "/f /im timeporting.exe"; \
  Flags: runhidden skipifdoesntexist
Filename: "taskkill"; Parameters: "/f /im mockjira.exe"; \
  Flags: runhidden skipifdoesntexist

[Code]
// KillIfRunning force-terminates a process by image name (no error if absent).
procedure KillIfRunning(const ExeName: String);
var
  ResultCode: Integer;
begin
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM ' + ExeName, '',
    SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

// Terminate the app's processes before the installer scans for files in use.
// Running this in InitializeSetup means nothing holds our files open, so the
// "Preparing to Install / applications are using files" page never appears —
// this also makes silent auto-updates seamless.
function InitializeSetup(): Boolean;
begin
  KillIfRunning('tray.exe');
  KillIfRunning('timeporting.exe');
  KillIfRunning('mockjira.exe');
  Result := True;
end;
