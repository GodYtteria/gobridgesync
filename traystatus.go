package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func trayStatus(s Status, now time.Time) (line, badge string) {
	switch {
	case s.State == "setup" || s.State == "signin" || s.State == "login":
		return "● Setup needed: " + s.Message, "setup"
	case s.Paused:
		return "⏸ Paused", "paused"
	case s.State == "syncing":
		p := s.Progress
		if p.Phase == "uploading" && p.BytesTotal > 0 {
			return fmt.Sprintf("⟳ Uploading %d/%d files (%d%%)", p.FilesDone, p.FilesTotal, p.BytesDone*100/p.BytesTotal), "sync"
		}
		return "⟳ Checking files…", "sync"
	case len(s.Issues) > 0 || s.State == "error":
		return fmt.Sprintf("⚠ %d problem(s), open GoBridgeSync", len(s.Issues)), "error"
	case s.State == "idle":
		return "✓ Up to date" + ago(s.LastSync, now), "ok"
	}
	return "Starting…", "sync"
}

func ago(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return " · just now"
	case d < time.Hour:
		return fmt.Sprintf(" · %dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf(" · %dh ago", int(d.Hours()))
	}
	return " · " + t.Format("Jan 2")
}

func trayStorage(q *Quota) string {
	if q == nil {
		return "Drive storage: checking…"
	}
	if q.Limit <= 0 {
		return "Drive storage: " + humanBytes(q.Used) + " used"
	}
	return fmt.Sprintf("Drive storage: %s of %s (%d%%)", humanBytes(q.Used), humanBytes(q.Limit), q.Used*100/q.Limit)
}

func trayActive(badge string) bool { return badge == "ok" || badge == "sync" }

func shortHome(p, home string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		return "~" + p[len(home):]
	}
	return p
}
