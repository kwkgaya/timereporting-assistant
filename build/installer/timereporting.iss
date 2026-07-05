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

[Components]
Name: "core"; Description: "Core application (review UI + CLI)"; Types: full custom; Flags: fixed
Name: "mock"; Description: "Mock Jira server (safe local testing target)"; Types: full custom

[Files]
Source: "{#DistDir}\timeporting.exe"; DestDir: "{app}"; Flags: ignoreversion; Components: core
Source: "{#DistDir}\tray.exe"; DestDir: "{app}"; Flags: ignoreversion; Components: core
Source: "{#DistDir}\mockjira.exe"; DestDir: "{app}"; Flags: ignoreversion; Components: mock
Source: "{#DistDir}\config.example.json"; DestDir: "{app}"; Flags: ignoreversion; Components: core
Source: "{#DistDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion isreadme; Components: core

[Icons]
Name: "{group}\Timereporting Assistant"; Filename: "{app}\timeporting.exe"; Comment: "Review and submit your time reports"
Name: "{group}\Timereporting Tray"; Filename: "{app}\tray.exe"; Comment: "System-tray companion (also starts automatically at login)"
Name: "{group}\Mock Jira (inspect)"; Filename: "{app}\mockjira.exe"; Components: mock
Name: "{group}\Uninstall Timereporting Assistant"; Filename: "{uninstallexe}"
Name: "{userdesktop}\Timereporting Assistant"; Filename: "{app}\timeporting.exe"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; Flags: unchecked
Name: "autostart"; Description: "Start tray app when I log in (recommended)"; Flags: checkedonce

[Registry]
; Optional auto-start at login (per-user, no admin).
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; \
  ValueType: string; ValueName: "TimereportingAssistant"; \
  ValueData: """{app}\tray.exe"""; \
  Tasks: autostart; Flags: uninsdeletevalue

[Run]
Filename: "{app}\tray.exe"; Description: "Start tray app now (recommended)"; \
  Flags: nowait postinstall skipifsilent

[UninstallRun]
; Silently terminate all app processes before files are deleted.
; This prevents the "application has to be closed" prompt on uninstall.
Filename: "taskkill"; Parameters: "/f /im tray.exe"; \
  Flags: runhidden skipifdoesntexist
Filename: "taskkill"; Parameters: "/f /im timeporting.exe"; \
  Flags: runhidden skipifdoesntexist
Filename: "taskkill"; Parameters: "/f /im mockjira.exe"; \
  Flags: runhidden skipifdoesntexist
