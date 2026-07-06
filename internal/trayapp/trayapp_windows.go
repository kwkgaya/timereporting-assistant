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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows/registry"

	"github.com/kwkgaya/timereporting-assistant/internal/applog"
	"github.com/kwkgaya/timereporting-assistant/internal/config"
	"github.com/kwkgaya/timereporting-assistant/internal/updater"
)

//go:embed assets/icon.ico
var appIconPNG []byte

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
	systray.Run(func() { onReady(version, cfgPath) }, nil)
}

func onReady(version, cfgPath string) {
	systray.SetTitle("Timereporting")
	systray.SetTooltip("Timereporting Assistant " + version)
	setIcon()

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
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Exit Timereporting Assistant tray")

	// Show the reminder on the first time the user interacts with the computer
	// each day (i.e. returns from lock screen, sleep, or any idle ≥ 5 min).
	// Do NOT show it immediately on startup/login — that's distracting.
	go watchForFirstInteraction(func() {
		today := time.Now().Format("2006-01-02")
		checkAndRemind(cfg, loadState(), today)
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
			ensureServerRunning(cfg)
			openBrowser(webURL)
		case <-mOpenLogs.ClickedCh:
			openLogsFolder()
		case <-mUpdate.ClickedCh:
			go checkForUpdates(cfg, version, true)
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
	count := countIncompleteDays(cfg)
	if count <= 0 {
		return
	}
	msg := fmt.Sprintf("You have %d incomplete day(s). Click to review.", count)
	showReminderToast("⏰ Time reporting reminder", msg, fmt.Sprintf("http://localhost:%d", cfg.WebPort))
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

// ensureServerRunning starts timeporting if the review UI isn't already up.
// It blocks (up to ~30s) until the web port is accepting connections so the
// caller can open the browser without hitting a not-yet-listening port.
func ensureServerRunning(cfg config.Config) {
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.WebPort)
	if portOpen(addr) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("ensureServerRunning: os.Executable: %v", err)
		return
	}
	name := "timeporting.exe"
	path := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(path); err != nil {
		log.Printf("ensureServerRunning: %s not found: %v", path, err)
		return
	}
	cmd := exec.Command(path, "--no-browser")
	// CREATE_NO_WINDOW prevents any console window from appearing.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		log.Printf("ensureServerRunning: start %s: %v", path, err)
		return
	}
	log.Printf("ensureServerRunning: started %s (pid %d), waiting for %s", path, cmd.Process.Pid, addr)

	// Wait for the server to finish building plans and start listening.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if portOpen(addr) {
			log.Printf("ensureServerRunning: %s is up", addr)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	log.Printf("ensureServerRunning: timed out waiting for %s", addr)
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

// ── Toast notifications ──────────────────────────────────────────────────────

// showReminderToast shows a prominent Windows toast for the daily reminder.
// It uses scenario="reminder" (stays on screen longer, plays reminder sound)
// with two action buttons: "Review now" and "Remind me later".
func showReminderToast(title, message, url string) {
	sanitise := func(s string) string {
		s = strings.ReplaceAll(s, `"`, `'`)
		s = strings.ReplaceAll(s, "`", "'")
		return s
	}
	title = sanitise(title)
	message = sanitise(message)
	url = sanitise(url)

	ps := fmt.Sprintf(`
Add-Type -AssemblyName System.Runtime.WindowsRuntime | Out-Null
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType=WindowsRuntime] | Out-Null
$template = @"
<toast scenario="reminder" duration="long" activationType="protocol" launch="%s">
  <visual>
    <binding template="ToastGeneric">
      <text hint-style="title">%s</text>
      <text hint-wrap="true">%s</text>
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
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("Timereporting Assistant").Show($toast)
`, url, title, message, url)

	cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", ps)
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
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("Timereporting Assistant").Show($toast)
`, url, title, message)

	cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", ps)
	_ = cmd.Start()
}

// ── Icon ──────────────────────────────────────────────────────────────────────

// setIcon sets the tray icon from the embedded PNG asset.
func setIcon() {
	systray.SetIcon(appIconPNG)
}

// openBrowser opens url in the default browser.
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
			showToast("Timereporting", "You're on the latest version ("+version+").", "")
		}
		return
	}
	log.Printf("update available: %s (current %s)", rel.TagName, version)
	if manual {
		showToast("Updating Timereporting Assistant", "Downloading "+rel.TagName+"…", "")
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
