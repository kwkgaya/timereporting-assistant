//go:build windows

// Package trayapp implements the Windows system-tray icon, once-per-day
// reminder toast, auto-start registration, and the first-launch-time recorder.
package trayapp

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/template"
	"time"
	"unsafe"

	"sync"

	"fyne.io/systray"
	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows/registry"

	"github.com/kwkgaya/timereporting-assistant/internal/applog"
	"github.com/kwkgaya/timereporting-assistant/internal/config"
	"github.com/kwkgaya/timereporting-assistant/internal/updater"
)

//go:embed assets/icon.ico
var appIconPNG []byte

//go:embed assets/toast-logo.png
var toastLogoPNG []byte

const (
	autoStartKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
	autoStartName = "TimereportingAssistant"
	stateDir      = "timereporting-assistant"
	stateFile     = "state.json"
)

// state persists per-day timestamps across runs.
type state struct {
	LastRemindedDate string            `json:"lastRemindedDate"` // YYYY-MM-DD
	FirstLaunch      map[string]string `json:"firstLaunch"`      // YYYY-MM-DD -> HH:MM
}

// Run starts the tray icon and blocks until the user quits.
func Run(version, cfgPath string) {
	if !claimSingleInstance() {
		log.Printf("another Time Reporting Assistant tray is already running — exiting")
		return
	}
	systray.Run(func() { onReady(version, cfgPath) }, nil)
}

// claimSingleInstance reports whether this is the only tray process in the
// current session. The mutex handle is deliberately never closed: it must live
// for the whole process, and Windows releases it automatically on exit.
func claimSingleInstance() bool {
	name, err := syscall.UTF16PtrFromString(`Local\TimereportingAssistantTray`)
	if err != nil {
		return true
	}
	h, _, lastErr := procCreateMutex.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return true
	}
	const errorAlreadyExists = syscall.Errno(183)
	return lastErr != error(errorAlreadyExists)
}

func onReady(version, cfgPath string) {
	systray.SetTitle("Time Reporting")
	systray.SetTooltip("Time Reporting Assistant " + version)
	setIcon()
	registerProtocolHandler()

	cfg, _ := config.Load(cfgPath)
	if cfg.WebPort == 0 {
		cfg.WebPort = 9080
	}
	if cfg.MockJiraPort == 0 {
		cfg.MockJiraPort = 9099
	}

	// Record first-launch-today time.
	s := loadState()
	today := time.Now().Format("2006-01-02")
	if s.FirstLaunch == nil {
		s.FirstLaunch = map[string]string{}
	}
	if s.FirstLaunch[today] == "" {
		s.FirstLaunch[today] = time.Now().Format("15:04")
		saveState(s)
	}

	// Menu items.
	mOpenReport := systray.AddMenuItem("Open time report", "Open the review UI in your browser")
	mOpenLogs := systray.AddMenuItem("Open logs folder", "Open the folder containing the log file")
	mUpdate := systray.AddMenuItem("Check for updates now", "Check GitHub for a newer version")
	systray.AddSeparator()
	mAutoStart := systray.AddMenuItemCheckbox("Start at login", "Toggle auto-start at Windows login", isAutoStartRegistered())
	mVersion := systray.AddMenuItem("Version: "+version, "")
	mVersion.Disable()
	mTestReminder := systray.AddMenuItem("Test reminder toast", "Preview the daily reminder toast")
	if !isBetaVersion(version) {
		mTestReminder.Hide()
	}
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Exit Time Reporting Assistant tray")

	// Show the reminder on the first time the user interacts with the computer
	// each day (i.e. returns from lock screen, sleep, or any idle ≥ 5 min).
	// Do NOT show it immediately on startup/login — that's distracting.
	go watchForFirstInteraction(func() {
		today := time.Now().Format("2006-01-02")
		checkAndRemind(cfg, loadState(), today)
	})

	// Clicking the reminder toast relaunches this exe (via the timereporting://
	// protocol) rather than talking to this process directly, so it signals
	// this running instance through a named event instead.
	go watchForOpenReportSignal(func(path string) {
		url := fmt.Sprintf("http://localhost:%d%s", cfg.WebPort, path)
		openAppWindow("Time Reporting Assistant", url, func() error { return ensureServerRunning(cfg) })
	})

	// Auto-update check shortly after startup (if enabled and this is a
	// released build).
	go func() {
		time.Sleep(5 * time.Second)
		maybeAutoUpdate(cfg, version)
	}()

	webURL := fmt.Sprintf("http://localhost:%d", cfg.WebPort)

	for {
		select {
		case <-mOpenReport.ClickedCh:
			// Starting the server can take tens of seconds; the window opens
			// immediately with a spinner and waits for it there.
			openAppWindow("Time Reporting Assistant", webURL, func() error { return ensureServerRunning(cfg) })
		case <-mOpenLogs.ClickedCh:
			openLogsFolder()
		case <-mUpdate.ClickedCh:
			go checkForUpdates(cfg, version, true)
		case <-mTestReminder.ClickedCh:
			// Fires the toast directly, bypassing the incomplete-day count and
			// once-per-day gate, so it always shows for previewing.
			go showReminderToast("⏰ Time reporting reminder", "Test toast — you have 3 incomplete day(s). Click to review.", "")
		case <-mAutoStart.ClickedCh:
			if mAutoStart.Checked() {
				_ = UnregisterAutoStart()
				mAutoStart.Uncheck()
			} else {
				_ = RegisterAutoStart()
				mAutoStart.Check()
			}
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// checkAndRemind shows a toast once per day if there are incomplete days.
func checkAndRemind(cfg config.Config, s state, today string) {
	if s.LastRemindedDate == today {
		return
	}
	// The server must be running to count incomplete days.
	if err := ensureServerRunning(cfg); err != nil {
		log.Printf("checkAndRemind: %v", err)
		return
	}

	// Check for a credential error first; warn even when count is zero.
	if credErr := fetchCredentialError(cfg); credErr != "" {
		showReminderToast("⚠️ Jira credentials error", credErr+" — open Settings to fix it.", "/settings")
		s.LastRemindedDate = today
		saveState(s)
		return
	}

	count := countIncompleteDays(cfg)
	if count <= 0 {
		return
	}
	msg := fmt.Sprintf("You have %d incomplete day(s). Click to review.", count)
	showReminderToast("⏰ Time reporting reminder", msg, "")
	s.LastRemindedDate = today
	saveState(s)
}

// watchForFirstInteraction polls the system idle time. When the user has been
// idle for ≥ idleThreshold (indicating a lock/sleep/walk-away) and then
// returns, it fires onReturn once, then waits for the next idle→active cycle.
// This fires on the user's first real interaction of the day after being away,
// not on login.
func watchForFirstInteraction(onReturn func()) {
	const idleThreshold = 5 * time.Minute
	const poll = 20 * time.Second

	user32 := syscall.MustLoadDLL("user32.dll")
	getLastInput := user32.MustFindProc("GetLastInputInfo")
	kernel32 := syscall.MustLoadDLL("kernel32.dll")
	getTickCount := kernel32.MustFindProc("GetTickCount")

	type LASTINPUTINFO struct {
		cbSize uint32
		dwTime uint32
	}

	wasIdle := false
	for {
		time.Sleep(poll)
		var lii LASTINPUTINFO
		lii.cbSize = uint32(unsafe.Sizeof(lii))
		getLastInput.Call(uintptr(unsafe.Pointer(&lii)))
		tick, _, _ := getTickCount.Call()
		idleMs := uint32(tick) - lii.dwTime
		idleDur := time.Duration(idleMs) * time.Millisecond

		if idleDur >= idleThreshold {
			if !wasIdle {
				log.Printf("user became idle (idle=%s)", idleDur.Round(time.Second))
			}
			wasIdle = true
		} else if wasIdle {
			// User just returned from idle — first interaction.
			wasIdle = false
			log.Printf("user returned from idle — firing reminder check")
			go onReturn()
		}
	}
}

// fetchCredentialError queries /api/status and returns the credError field, or
// empty string when the server is unreachable or credentials are fine.
func fetchCredentialError(cfg config.Config) string {
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/api/status", cfg.WebPort))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var st struct {
		CredError string `json:"credError"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return ""
	}
	return st.CredError
}

// countIncompleteDays asks the running web server how many days are under 7h.
// If the server isn't running it returns 0 (no spurious reminders).
func countIncompleteDays(cfg config.Config) int {
	url := fmt.Sprintf("http://localhost:%d/api/days", cfg.WebPort)
	resp, err := http.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var days []struct {
		Existing  []struct{ Minutes int } `json:"existing"`
		Suggested []struct{ Minutes int } `json:"suggested"`
		Status    string                  `json:"status"`
		Submitted bool                    `json:"submitted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&days); err != nil {
		return 0
	}
	target := int(cfg.WorkdayHours * 60)
	if target == 0 {
		target = 420
	}
	count := 0
	for _, d := range days {
		if d.Status == "holiday" || d.Status == "full_leave" {
			continue
		}
		if d.Submitted {
			continue
		}
		total := 0
		for _, w := range d.Existing {
			total += w.Minutes
		}
		if total < target {
			count++
		}
	}
	return count
}

// startAttempt is one in-flight server start, shared by every caller that
// arrives while it runs.
type startAttempt struct {
	done chan struct{}
	err  error // read only after done is closed
}

var (
	serverStartMu sync.Mutex
	serverStart   *startAttempt // non-nil while a start is in flight
)

// ensureServerRunning starts timeporting if the review UI isn't already up.
// It blocks (up to ~60s) until the web port is accepting connections so the
// caller can open the browser without hitting a not-yet-listening port.
// Concurrent callers (tray click and reminder check) join the same attempt
// rather than queueing behind each other for another full startup.
func ensureServerRunning(cfg config.Config) error {
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.WebPort)
	if portOpen(addr) {
		return nil
	}

	serverStartMu.Lock()
	if a := serverStart; a != nil {
		serverStartMu.Unlock()
		<-a.done
		return a.err
	}
	a := &startAttempt{done: make(chan struct{})}
	serverStart = a
	serverStartMu.Unlock()

	a.err = startServer(addr)

	serverStartMu.Lock()
	serverStart = nil
	serverStartMu.Unlock()
	close(a.done)
	return a.err
}

// startServer spawns timeporting.exe and waits for it to listen on addr.
func startServer(addr string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate the installation folder: %w", err)
	}
	path := filepath.Join(filepath.Dir(exe), "timeporting.exe")
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("timeporting.exe was not found next to the tray app (%s) — try reinstalling", path)
	}
	cmd := exec.Command(path, "--no-browser")
	// CREATE_NO_WINDOW prevents any console window from appearing.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start %s: %w", path, err)
	}
	pid := cmd.Process.Pid
	log.Printf("startServer: started %s (pid %d), waiting for %s", path, pid, addr)

	// Reaps the child and records why it went away; it ends with the process.
	// The buffer keeps it from blocking once the startup loop has moved on.
	exited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		log.Printf("startServer: %s (pid %d) exited: %v", path, pid, err)
		exited <- err
	}()

	// Wait for the server to finish building plans and start listening.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if portOpen(addr) {
			log.Printf("startServer: %s is up", addr)
			return nil
		}
		select {
		case err := <-exited:
			if portOpen(addr) {
				return nil
			}
			return fmt.Errorf("the background service stopped during startup (%v) — see the logs for details", err)
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("the background service did not start listening on %s within 60s — see the logs for details", addr)
}

// portOpen reports whether a TCP connection to addr succeeds quickly.
func portOpen(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// ── Auto-start (per-user registry, no admin) ─────────────────────────────────

// RegisterAutoStart adds the tray binary to the per-user Run registry key.
func RegisterAutoStart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, autoStartKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(autoStartName, `"`+exe+`"`)
}

// UnregisterAutoStart removes the tray binary from the per-user Run key.
func UnregisterAutoStart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoStartKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	err = k.DeleteValue(autoStartName)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

func isAutoStartRegistered() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoStartKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autoStartName)
	return err == nil && v != ""
}

// ── State persistence ─────────────────────────────────────────────────────────

func stateFilePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, stateDir, stateFile)
}

func loadState() state {
	var s state
	data, err := os.ReadFile(stateFilePath())
	if err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.FirstLaunch == nil {
		s.FirstLaunch = map[string]string{}
	}
	return s
}

func saveState(s state) {
	path := stateFilePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(path, data, 0o600)
}

// ── Toast click → app window ─────────────────────────────────────────────────
//
// A toast can only "protocol activate" a registered URI scheme; a bare
// http:// launch target is handed to the default browser instead of this app
// (#97). So toasts launch "timereporting://open-report[?path=...]", which
// registerProtocolHandler maps to relaunching this exe with --open-report.
// That short-lived second process can't reach into the already-running tray
// directly, so it hands off the request via a named event plus a small file
// carrying the target path, and watchForOpenReportSignal (running in the
// original process) picks it up and opens the app window.

const (
	openReportEventName = `Local\TimereportingAssistantOpenReport`
	openReportProtocol  = "timereporting"
)

// registerProtocolHandler registers the timereporting:// URI scheme (per-user,
// no admin) to relaunch this exe with --open-report. Best-effort: failures are
// silently ignored and simply leave toast clicks non-functional.
func registerProtocolHandler() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	base, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+openReportProtocol, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer base.Close()
	_ = base.SetStringValue("", "URL:Time Reporting Assistant")
	_ = base.SetStringValue("URL Protocol", "")

	cmdKey, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+openReportProtocol+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer cmdKey.Close()
	_ = cmdKey.SetStringValue("", fmt.Sprintf(`"%s" --open-report "%%1"`, exe))
}

// openReportLaunchURI builds the toast's protocol-activation launch target
// for the given app-relative path (e.g. "" or "/settings").
func openReportLaunchURI(path string) string {
	u := openReportProtocol + "://open-report"
	if path != "" {
		u += "?path=" + url.QueryEscape(path)
	}
	return u
}

// openRequestFilePath is where a --open-report process leaves the requested
// path for the running tray to pick up; the named event carries no payload.
func openRequestFilePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, stateDir, "open-request.txt")
}

// SignalOpenReport asks a running tray instance to open the report window at
// path (e.g. "" or "/settings"). Called by the short-lived process launched
// via protocol activation; a no-op if no tray instance is there to receive it.
func SignalOpenReport(path string) {
	_ = os.MkdirAll(filepath.Dir(openRequestFilePath()), 0o755)
	_ = os.WriteFile(openRequestFilePath(), []byte(path), 0o600)

	name, err := syscall.UTF16PtrFromString(openReportEventName)
	if err != nil {
		return
	}
	h, _, _ := procCreateEvent.Call(0, 0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return
	}
	defer procCloseHandle.Call(h)
	procSetEvent.Call(h)
}

// watchForOpenReportSignal blocks waiting on the named event set by
// SignalOpenReport, invoking onOpen with the requested path each time.
func watchForOpenReportSignal(onOpen func(path string)) {
	name, err := syscall.UTF16PtrFromString(openReportEventName)
	if err != nil {
		return
	}
	h, _, _ := procCreateEvent.Call(0, 0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return
	}
	const infinite = 0xFFFFFFFF
	for {
		r, _, _ := procWaitForSingleObject.Call(h, infinite)
		if r != 0 {
			continue
		}
		path := ""
		if data, err := os.ReadFile(openRequestFilePath()); err == nil {
			path = string(data)
		}
		_ = os.Remove(openRequestFilePath())
		onOpen(path)
	}
}

// ── Toast notifications ──────────────────────────────────────────────────────

// toastImageFile writes the embedded app logo to a stable cache-dir path once
// and returns its path, for use as the toast's hero/logo image. The file is
// kept (not deleted) because the toast is rendered asynchronously by a
// detached powershell process, well after this function returns. Returns ""
// — and the toast falls back to text-only — if the write fails.
var (
	toastImageOnce sync.Once
	toastImagePath string
)

func toastImageFile() string {
	toastImageOnce.Do(func() {
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = os.TempDir()
		}
		dir = filepath.Join(dir, stateDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		path := filepath.Join(dir, "toast-logo.png")
		if err := os.WriteFile(path, toastLogoPNG, 0o644); err != nil {
			return
		}
		toastImagePath = path
	})
	return toastImagePath
}

// showReminderToast shows a large, hard-to-miss Windows toast for the daily
// reminder: a hero banner and big logo make it visually much bigger than a
// plain two-line toast, scenario="urgent" breaks through Focus Assist/Do Not
// Disturb (Windows 11+) and keeps it on screen until dismissed, and it plays
// a looping reminder sound. Two action buttons: "Review now" and "Dismiss".
// path is the app-relative page to open on click (e.g. "" or "/settings").
func showReminderToast(title, message, path string) {
	sanitise := func(s string) string {
		s = strings.ReplaceAll(s, `"`, `'`)
		s = strings.ReplaceAll(s, "`", "'")
		return s
	}
	title = sanitise(title)
	message = sanitise(message)
	launch := sanitise(openReportLaunchURI(path))

	images := ""
	if imgPath := toastImageFile(); imgPath != "" {
		imgURI := "file:///" + strings.ReplaceAll(imgPath, `\`, "/")
		images = fmt.Sprintf(`
      <image placement="hero" src="%s"/>
      <image placement="appLogoOverride" hint-crop="none" src="%s"/>`, imgURI, imgURI)
	}

	ps := fmt.Sprintf(`
Add-Type -AssemblyName System.Runtime.WindowsRuntime | Out-Null
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType=WindowsRuntime] | Out-Null
$template = @"
<toast scenario="urgent" duration="long" activationType="protocol" launch="%s">
  <visual>
    <binding template="ToastGeneric">
      <text hint-style="title" hint-wrap="true">%s</text>
      <text hint-style="body" hint-wrap="true">%s</text>
      <text hint-style="captionSubtle" hint-wrap="true">Stays on screen until you review it.</text>%s
    </binding>
  </visual>
  <actions>
    <action content="Review now" activationType="protocol" arguments="%s"/>
    <action content="Dismiss" activationType="system" arguments="dismiss"/>
  </actions>
</toast>
"@
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml($template)
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("Time Reporting Assistant").Show($toast)
`, launch, title, message, images, launch)

	cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	_ = cmd.Start()
}

// showToast shows a simple Windows toast (used for non-reminder notifications).
func showToast(title, message, url string) {
	// Sanitise inputs for embedding in PowerShell string.
	sanitise := func(s string) string {
		s = strings.ReplaceAll(s, `"`, `'`)
		s = strings.ReplaceAll(s, "`", "'")
		return s
	}
	title = sanitise(title)
	message = sanitise(message)
	url = sanitise(url)

	// Use Windows.UI.Notifications via PowerShell (works on Win 10+, no extra deps).
	ps := fmt.Sprintf(`
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType=WindowsRuntime] | Out-Null
$template = @"
<toast activationType="protocol" launch="%s">
  <visual><binding template="ToastGeneric">
    <text>%s</text>
    <text>%s</text>
  </binding></visual>
</toast>
"@
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml($template)
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("Time Reporting Assistant").Show($toast)
`, url, title, message)

	cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	_ = cmd.Start()
}

// ── Icon ──────────────────────────────────────────────────────────────────────

// setIcon sets the tray icon from the embedded PNG asset.
func setIcon() {
	systray.SetIcon(appIconPNG)
}

// Only one app window exists at a time; clicking the tray item while the
// window is already open restores and focuses it instead of opening a second.
var (
	webviewMu   sync.Mutex
	webviewHWND uintptr // 0 when no window is open
	// WebView2 takes seconds to initialise, during which webviewHWND is still
	// 0; without this flag a second tray click opens a second window.
	webviewOpening bool
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procIsWindow         = user32.NewProc("IsWindow")
	procBringWindowToTop = user32.NewProc("BringWindowToTop")
	procLoadImage        = user32.NewProc("LoadImageW")
	procSendMessage      = user32.NewProc("SendMessageW")
	procSetClassLongPtr  = user32.NewProc("SetClassLongPtrW")
	procIsIconic         = user32.NewProc("IsIconic")
	procGetForeground    = user32.NewProc("GetForegroundWindow")
	procGetWindowThread  = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInpt = user32.NewProc("AttachThreadInput")
	procSetActiveWindow  = user32.NewProc("SetActiveWindow")

	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex         = kernel32.NewProc("CreateMutexW")
	procCurrentThreadID     = kernel32.NewProc("GetCurrentThreadId")
	procCreateEvent         = kernel32.NewProc("CreateEventW")
	procSetEvent            = kernel32.NewProc("SetEvent")
	procWaitForSingleObject = kernel32.NewProc("WaitForSingleObject")
	procCloseHandle         = kernel32.NewProc("CloseHandle")
)

const (
	swRestore = 9
	swShow    = 5
)

// Icons are loaded once and reused: WM_SETICON does not take ownership, so the
// handles must stay alive for as long as any window uses them.
var (
	iconOnce         sync.Once
	hIconBig, hIconS uintptr
)

// loadAppIcons writes the embedded .ico to a temp file and loads the large and
// small icon handles from it.
func loadAppIcons() {
	tmp, err := os.CreateTemp("", "*.ico")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(appIconPNG); err != nil {
		tmp.Close()
		return
	}
	tmp.Close()

	path16, err := syscall.UTF16PtrFromString(tmpPath)
	if err != nil {
		return
	}
	const (
		imageIcon      = 1
		lrLoadFromFile = 0x10
	)
	hIconBig, _, _ = procLoadImage.Call(0, uintptr(unsafe.Pointer(path16)), imageIcon, 32, 32, lrLoadFromFile)
	hIconS, _, _ = procLoadImage.Call(0, uintptr(unsafe.Pointer(path16)), imageIcon, 16, 16, lrLoadFromFile)
}

// setWindowIcon sets the title-bar and Alt-Tab icons on hwnd.
func setWindowIcon(hwnd uintptr) {
	iconOnce.Do(loadAppIcons)
	const (
		wmSetIcon   = 0x0080
		iconSmall   = 0
		iconBig     = 1
		gclpHIcon   = ^uintptr(13) // -14
		gclpHIconSm = ^uintptr(33) // -34
	)
	// go-webview2 registers its window class with IDI_APPLICATION; the taskbar
	// falls back to that class icon, so it must be replaced too.
	if hIconBig != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconBig, hIconBig)
		procSetClassLongPtr.Call(hwnd, gclpHIcon, hIconBig)
	}
	if hIconS != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconSmall, hIconS)
		procSetClassLongPtr.Call(hwnd, gclpHIconSm, hIconS)
	}
}

// bringWindowToFront un-minimises hwnd and puts it in front of everything.
// Windows refuses SetForegroundWindow from a process that does not already own
// the foreground window — a tray click leaves the shell in front, so the plain
// call is silently downgraded to a taskbar flash. Attaching this thread's input
// queue to the foreground thread for the duration is the documented way to lift
// that restriction.
func bringWindowToFront(hwnd uintptr) {
	// AttachThreadInput works on the calling OS thread, so it must not migrate.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
	} else {
		procShowWindow.Call(hwnd, swShow)
	}

	fg, _, _ := procGetForeground.Call()
	if fg == hwnd {
		return
	}
	ourThread, _, _ := procCurrentThreadID.Call()
	fgThread, _, _ := procGetWindowThread.Call(fg, 0)
	attached := false
	if fgThread != 0 && fgThread != ourThread {
		r, _, _ := procAttachThreadInpt.Call(ourThread, fgThread, 1)
		attached = r != 0
	}
	procBringWindowToTop.Call(hwnd)
	procSetForeground.Call(hwnd)
	procSetActiveWindow.Call(hwnd)
	if attached {
		procAttachThreadInpt.Call(ourThread, fgThread, 0)
	}
}

// splashHTML is shown while `wait` runs so the window is never a blank frame.
const splashHTML = `<!doctype html><html><head><meta charset="utf-8"><style>
html,body{height:100%;margin:0}
body{display:flex;align-items:center;justify-content:center;
 font:14px/1.5 "Segoe UI",system-ui,sans-serif;color:#334155;background:#f8fafc}
.box{text-align:center;max-width:420px;padding:24px}
.spin{width:38px;height:38px;margin:0 auto 18px;border:4px solid #dbe3ec;
 border-top-color:#2563eb;border-radius:50%;animation:r .9s linear infinite}
@keyframes r{to{transform:rotate(360deg)}}
h1{font-size:16px;font-weight:600;margin:0 0 6px;color:#0f172a}
p{margin:0;color:#64748b}
</style></head><body><div class="box"><div class="spin"></div>
<h1>Starting Time Reporting Assistant…</h1>
<p>Collecting git activity, calendar events and Jira issues. This can take up to a minute on first launch.</p>
</div></body></html>`

// errorHTML renders a startup failure inside the window instead of leaving the
// user with an unresponsive blank frame.
func errorHTML(msg string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><style>
html,body{height:100%;margin:0}
body{display:flex;align-items:center;justify-content:center;
 font:14px/1.5 "Segoe UI",system-ui,sans-serif;color:#334155;background:#f8fafc}
.box{max-width:520px;padding:24px}
h1{font-size:16px;font-weight:600;margin:0 0 10px;color:#b91c1c}
p{margin:0 0 10px;color:#475569}
code{display:block;background:#fff;border:1px solid #e2e8f0;border-radius:6px;
 padding:10px;white-space:pre-wrap;word-break:break-word;color:#0f172a}
</style></head><body><div class="box">
<h1>Couldn't start Time Reporting Assistant</h1>
<code>` + template.HTMLEscapeString(msg) + `</code>
<p>Close this window and try “Open time report” again. Use “Open logs folder” in the tray menu for details.</p>
</div></body></html>`
}

// openAppWindow shows the app window, displaying a spinner until wait returns.
// wait runs off the UI thread; if it fails the error is rendered in the window.
func openAppWindow(title, url string, wait func() error) {
	webviewMu.Lock()
	// A window is already being created — the click is a no-op.
	if webviewOpening {
		webviewMu.Unlock()
		return
	}
	if hwnd := webviewHWND; hwnd != 0 {
		if alive, _, _ := procIsWindow.Call(hwnd); alive != 0 {
			webviewMu.Unlock()
			bringWindowToFront(hwnd)
			return
		}
	}
	// No window, or the handle is stale — open a fresh one.
	webviewHWND = 0
	webviewOpening = true
	webviewMu.Unlock()

	go func() {
		// The window and its message loop must live on the same OS thread.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("app window panicked: %v", rec)
			}
			webviewMu.Lock()
			webviewHWND = 0
			webviewOpening = false
			webviewMu.Unlock()
		}()

		w := webview.New(false)
		if w == nil {
			// WebView2 runtime not available — fall back to system browser.
			log.Printf("WebView2 not available, opening system browser")
			if err := wait(); err != nil {
				log.Printf("startup failed: %v", err)
				showToast("Time Reporting Assistant", err.Error(), "")
				return
			}
			openBrowser(url)
			return
		}
		defer w.Destroy()
		w.SetTitle(title)
		w.SetSize(1280, 860, webview.HintNone)

		// Store the HWND so we can focus the window on subsequent tray clicks.
		webviewMu.Lock()
		webviewHWND = uintptr(w.Window())
		setWindowIcon(webviewHWND)
		webviewMu.Unlock()

		w.SetHtml(splashHTML)

		go func() {
			var err error
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						err = fmt.Errorf("unexpected error during startup: %v", rec)
					}
				}()
				err = wait()
			}()
			w.Dispatch(func() {
				if err != nil {
					log.Printf("startup failed: %v", err)
					w.SetHtml(errorHTML(err.Error()))
					return
				}
				w.Navigate(url)
			})
		}()

		w.Run()
	}()
}

// openBrowser opens url in the default system browser (fallback).
func openBrowser(url string) {
	if runtime.GOOS == "windows" {
		_ = exec.Command("cmd", "/c", "start", "", url).Start()
	}
}

// openLogsFolder opens the directory containing the log file in Explorer.
func openLogsFolder() {
	dir := applog.LogDir()
	_ = os.MkdirAll(dir, 0o700)
	cmd := exec.Command("explorer", dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	_ = cmd.Start()
}

// isBetaVersion reports whether version is a pre-release build (e.g.
// "v0.33.0-beta.1"), used to gate dev-only tray menu items.
func isBetaVersion(version string) bool {
	return strings.Contains(strings.ToLower(version), "beta")
}

// maybeAutoUpdate runs an update check on startup when auto-update is enabled
// and this is a released (v-prefixed) build.
func maybeAutoUpdate(cfg config.Config, version string) {
	if !cfg.AutoUpdate {
		log.Printf("auto-update disabled")
		return
	}
	if !strings.HasPrefix(version, "v") {
		log.Printf("auto-update skipped for non-release build %q", version)
		return
	}
	checkForUpdates(cfg, version, false)
}

// checkForUpdates queries GitHub for a newer release and, if found, downloads
// and launches the installer silently. When manual is true, the outcome is
// surfaced via toast notifications.
func checkForUpdates(cfg config.Config, version string, manual bool) {
	chk := updater.New()
	rel, err := chk.Latest(version, cfg.UpdatePrerelease)
	if err != nil {
		log.Printf("update check failed: %v", err)
		if manual {
			showToast("Update check failed", err.Error(), "")
		}
		return
	}
	if rel == nil {
		log.Printf("no update available (current %s)", version)
		if manual {
			showToast("Time Reporting", "You're on the latest version ("+version+").", "")
		}
		return
	}
	log.Printf("update available: %s (current %s)", rel.TagName, version)
	// Build a short summary of release notes (first 3 non-empty lines).
	releaseNotes := ""
	if rel.Body != "" {
		lines := strings.Split(rel.Body, "\n")
		var summary []string
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l != "" && !strings.HasPrefix(l, "#") {
				summary = append(summary, l)
				if len(summary) == 3 {
					break
				}
			}
		}
		if len(summary) > 0 {
			releaseNotes = strings.Join(summary, " · ")
		}
	}
	toastBody := "Downloading " + rel.TagName + "…"
	if releaseNotes != "" {
		toastBody = rel.TagName + ": " + releaseNotes
	}
	if manual {
		showToast("Updating Time Reporting Assistant", toastBody, "")
	}
	dir := filepath.Join(os.TempDir(), "timereporting-update")
	path, err := chk.Download(rel, dir)
	if err != nil {
		log.Printf("update download failed: %v", err)
		if manual {
			showToast("Update failed", err.Error(), "")
		}
		return
	}
	log.Printf("launching installer %s", path)
	cmd := exec.Command(path, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		log.Printf("run installer failed: %v", err)
		if manual {
			showToast("Update failed", err.Error(), "")
		}
		return
	}
	// The installer closes this tray (InitializeSetup) and relaunches it after
	// the files are replaced. Quit so we release our own binary's file lock.
	systray.Quit()
}
