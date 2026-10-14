package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

const (
	alertRepeat      = 6 * time.Hour
	quotaAlertRepeat = 24 * time.Hour
	quotaAlertPct    = 90
)

const windowsToast = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$x = $t.GetElementsByTagName('text')
$x.Item(0).AppendChild($t.CreateTextNode($env:GBS_TITLE)) > $null
$x.Item(1).AppendChild($t.CreateTextNode($env:GBS_BODY)) > $null
$id = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($id).Show([Windows.UI.Notifications.ToastNotification]::new($t))`

func desktopNotify(title, body string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("osascript", "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body)
	case "windows":
		cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", windowsToast)
		cmd.Env = append(os.Environ(), "GBS_TITLE="+title, "GBS_BODY="+body)
	default:
		cmd = exec.Command("notify-send", "--app-name=GoBridgeSync", "--icon=gobridgesync", title, body)
	}
	return cmd.Run()
}

func shouldAlert(prev string, prevAt, now time.Time, msg string) bool {
	return msg != prev || now.Sub(prevAt) >= alertRepeat
}

func issueSummary(issues []string) string {
	if len(issues) == 1 {
		return issues[0]
	}
	return fmt.Sprintf("%s (and %d more)", issues[0], len(issues)-1)
}

func quotaWarning(q *Quota) string {
	if q == nil || q.Limit <= 0 || q.Used*100/q.Limit < quotaAlertPct {
		return ""
	}
	return fmt.Sprintf("Google Drive is %d%% full: %s free. Free up space or upgrade storage.", q.Used*100/q.Limit, humanBytes(max(q.Limit-q.Used, 0)))
}
