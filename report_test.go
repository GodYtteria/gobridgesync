package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedact(t *testing.T) {
	in := `upload /home/erishsc/GoogleDrive/SD-Backup/Files/secret-plan.docx: token=abc123 ya29.AbCdEf_ghi https://www.googleapis.com/drive/v3/files`
	got := redact(in)
	for _, bad := range []string{"erishsc", "SD-Backup", "abc123", "ya29.AbCdEf"} {
		if strings.Contains(got, bad) {
			t.Fatalf("leaked %q in %q", bad, got)
		}
	}
	if !strings.Contains(got, "…/secret-plan.docx") || !strings.Contains(got, "https://www.googleapis.com/drive/v3/files") {
		t.Fatalf("lost useful detail: %q", got)
	}
}

func TestReporterDedupesAndRetriesFailures(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "app.log")
	os.WriteFile(logFile, []byte("line one /home/x/private/file.txt\nline two\n"), 0o600)
	var bodies []map[string]string
	fail := false
	r := newReporter(logFile)
	r.url = "https://reports.test/report"
	r.post = func(_ context.Context, _ string, b []byte) error {
		if fail {
			return errors.New("offline")
		}
		var m map[string]string
		json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		return nil
	}
	now := time.Now()
	if n := r.send(context.Background(), []string{"upload a.txt: 503", "upload b.txt: 503"}, now); n != 2 {
		t.Fatalf("sent %d", n)
	}
	if n := r.send(context.Background(), []string{"upload a.txt: 503"}, now.Add(time.Hour)); n != 0 {
		t.Fatal("same problem resent within a day")
	}
	fail = true
	if n := r.send(context.Background(), []string{"new problem 1"}, now); n != 0 {
		t.Fatal("failed send counted")
	}
	fail = false
	if n := r.send(context.Background(), []string{"new problem 1"}, now); n != 1 {
		t.Fatal("failed send should be retried")
	}
	if strings.Contains(bodies[0]["log"], "/home/x/private") || bodies[0]["version"] == "" {
		t.Fatalf("bad body %+v", bodies[0])
	}
}

func TestReporterDisabledWithoutURL(t *testing.T) {
	r := newReporter(filepath.Join(t.TempDir(), "app.log"))
	r.url = ""
	r.post = func(context.Context, string, []byte) error { t.Fatal("posted without a collector URL"); return nil }
	if r.send(context.Background(), []string{"boom"}, time.Now()) != 0 {
		t.Fatal("sent without a collector URL")
	}
}

func TestReportDefaults(t *testing.T) {
	if (AppConfig{}).sendReports() || !(AppConfig{}).reportsUndecided() {
		t.Fatal("open source edition must ask before sending")
	}
	yes := true
	if !(AppConfig{Reports: &yes}).sendReports() {
		t.Fatal("opt-in ignored")
	}
}

func TestRedactKeepsDates(t *testing.T) {
	got := redact("2026/10/15 22:33:27 upload /home/x/a.txt")
	if !strings.HasPrefix(got, "2026/10/15 22:33:27") || !strings.Contains(got, "…/a.txt") {
		t.Fatalf("got %q", got)
	}
}

func TestStatusHidesReportsWithoutCollector(t *testing.T) {
	if reportURL != "" {
		t.Skip("collector configured")
	}
	r := newRunner(Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}, false)
	yes := true
	if err := (AppConfig{Reports: &yes}).save(r.paths); err != nil {
		t.Fatal(err)
	}
	st := r.Snapshot()
	if st.ReportsReady || st.SendReports || st.AskReports {
		t.Fatalf("reports offered without a collector: %+v", st)
	}
}
