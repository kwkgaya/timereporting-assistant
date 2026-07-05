//go:build windows

// Package trayapp implements the Windows system-tray icon, once-per-day
// reminder toast, auto-start registration, and the first-launch-time recorder.
package trayapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows/registry"

	"github.com/kwkgaya/timereporting-assistant/internal/config"
)

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
		cfg.WebPort = 8080
	}
	if cfg.MockJiraPort == 0 {
		cfg.MockJiraPort = 8099
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
	mOpenMock := systray.AddMenuItem("Mock Jira inspect", "Open the mock Jira inspect page")
	systray.AddSeparator()
	mAutoStart := systray.AddMenuItemCheckbox("Start at login", "Toggle auto-start at Windows login", isAutoStartRegistered())
	mVersion := systray.AddMenuItem("Version: "+version, "")
	mVersion.Disable()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Exit Timereporting Assistant tray")

	// Once-per-day reminder.
	go func() {
		time.Sleep(3 * time.Second) // let the system settle after login
		checkAndRemind(cfg, s, today)
	}()

	webURL := fmt.Sprintf("http://localhost:%d", cfg.WebPort)
	mockURL := fmt.Sprintf("http://localhost:%d", cfg.MockJiraPort)

	for {
		select {
		case <-mOpenReport.ClickedCh:
			ensureServerRunning(cfg)
			openBrowser(webURL)
		case <-mOpenMock.ClickedCh:
			openBrowser(mockURL)
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
	showToast("⏰ Timereporting", msg, fmt.Sprintf("http://localhost:%d", cfg.WebPort))
	s.LastRemindedDate = today
	saveState(s)
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
func ensureServerRunning(cfg config.Config) {
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.WebPort)
	if c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
		_ = c.Close()
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	name := "timeporting.exe"
	path := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(path); err != nil {
		return
	}
	cmd := exec.Command(path, "--target", "mock")
	_ = cmd.Start()
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

// ── Toast notification ────────────────────────────────────────────────────────

// showToast shows a Windows toast notification using PowerShell.
// Clicking the toast opens url in the default browser.
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

// setIcon generates and sets a valid tray icon using stdlib image/png —
// no external asset files required. Replace with go:embed + a real .ico
// for a branded icon.
func setIcon() {
	systray.SetIcon(generateIcon())
}

// generateIcon creates a valid 16×16 RGBA PNG: Jira-blue background with a
// white "T" (for Timereporting) drawn as a simple pixel pattern.
func generateIcon() []byte {
	const size = 16
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := color.RGBA{R: 0, G: 82, B: 204, A: 255}    // #0052cc — Jira blue
	fg := color.RGBA{R: 255, G: 255, B: 255, A: 255} // white

	// Fill background.
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, bg)
		}
	}
	// Draw white "T": horizontal bar at y=3–4, vertical stem at x=7–8.
	for x := 3; x <= 12; x++ {
		img.Set(x, 3, fg)
		img.Set(x, 4, fg)
	}
	for y := 3; y <= 13; y++ {
		img.Set(7, y, fg)
		img.Set(8, y, fg)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		// Fallback: 1×1 transparent PNG — always valid.
		tiny := image.NewRGBA(image.Rect(0, 0, 1, 1))
		_ = png.Encode(&buf, tiny)
	}
	return buf.Bytes()
}

// openBrowser opens url in the default browser.
func openBrowser(url string) {
	if runtime.GOOS == "windows" {
		_ = exec.Command("cmd", "/c", "start", "", url).Start()
	}
}
