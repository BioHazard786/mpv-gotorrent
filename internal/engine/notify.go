package engine

import (
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
)

// SendNotification sends a cross-platform desktop notification asynchronously.
func SendNotification(title, message string) {
	go func() {
		switch runtime.GOOS {
		case "linux":
			if path, err := exec.LookPath("notify-send"); err == nil {
				_ = exec.Command(path, title, message).Run()
			}
		case "darwin":
			cleanAppleScript := func(s string) string {
				s = strings.ReplaceAll(s, `\`, `\\`)
				return strings.ReplaceAll(s, `"`, `\"`)
			}
			script := fmt.Sprintf(`display notification "%s" with title "%s"`, cleanAppleScript(message), cleanAppleScript(title))
			_ = exec.Command("osascript", "-e", script).Run()
		case "windows":
			escapePS := func(s string) string {
				return strings.ReplaceAll(s, `'`, `''`)
			}
			psScript := fmt.Sprintf(
				`[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null; `+
					`$template = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02); `+
					`$textNodes = $template.GetElementsByTagName("text"); `+
					`$textNodes.Item(0).AppendChild($template.CreateTextNode('%s')) > $null; `+
					`$textNodes.Item(1).AppendChild($template.CreateTextNode('%s')) > $null; `+
					`$toast = [Windows.UI.Notifications.ToastNotification]::new($template); `+
					`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("mpv-gotorrent").Show($toast)`,
				escapePS(title), escapePS(message),
			)
			_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript).Run()
		}
		slog.Debug("desktop notification dispatched", "title", title, "message", message)
	}()
}
