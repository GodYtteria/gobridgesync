package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func sampleRemote() *Remote {
	return &Remote{
		Files: map[string]RemoteFile{
			"B/pc/Pictures/a.jpg":     {ID: "1", Size: 10},
			"B/pc/Pictures/sub/b.jpg": {ID: "2", Size: 20},
			"B/pc/Docs/c.txt":         {ID: "3", Size: 30},
		},
		Folders: map[string]string{"": "root", "B": "b", "B/pc": "pc", "B/pc/Pictures": "p", "B/pc/Pictures/sub": "s", "B/pc/Docs": "d"},
	}
}

func TestBrowseRemote(t *testing.T) {
	got := browseRemote(sampleRemote(), "B/pc/Pictures")
	if len(got) != 2 || !got[0].Dir || got[0].Name != "sub" || got[1].Name != "a.jpg" || got[1].ID != "1" {
		t.Fatalf("got %+v", got)
	}
	if top := browseRemote(sampleRemote(), ""); len(top) != 1 || top[0].Path != "B" {
		t.Fatalf("top %+v", top)
	}
}

func TestRestoreItems(t *testing.T) {
	items := restoreItems(sampleRemote(), "B/pc/Pictures")
	if len(items) != 2 || items[0].rel != "a.jpg" || items[1].rel != "sub/b.jpg" {
		t.Fatalf("got %+v", items)
	}
	if one := restoreItems(sampleRemote(), "B/pc/Docs/c.txt"); len(one) != 1 || one[0].rel != "c.txt" {
		t.Fatalf("single %+v", one)
	}
	if none := restoreItems(sampleRemote(), "B/pc/Pic"); len(none) != 0 {
		t.Fatalf("prefix without slash matched: %+v", none)
	}
}

func TestFreeNameAndDest(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if got, _ := freeName(target); got != filepath.Join(dir, "a (restored).txt") {
		t.Fatalf("got %s", got)
	}
	os.WriteFile(filepath.Join(dir, "a (restored).txt"), nil, 0o644)
	if got, _ := freeName(target); got != filepath.Join(dir, "a (restored 2).txt") {
		t.Fatalf("got %s", got)
	}
	if _, err := restoreDest("relative/x"); err == nil {
		t.Fatal("relative destination accepted")
	}
	if d, err := restoreDest(""); err != nil || filepath.Base(d) != "GoBridgeSync Restore" {
		t.Fatalf("default dest %q %v", d, err)
	}
}

func TestNewRunnerInitialized(t *testing.T) {
	r := newRunner(Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}, false)
	if r.updater == nil || r.restart == nil || r.jobs == nil || r.trigger == nil || r.notify == nil {
		t.Fatalf("runner not fully initialized: %+v", r)
	}
	if err := r.QueueRestore(restoreJob{Source: "x", Dest: t.TempDir()}); err != nil {
		t.Fatalf("queue: %v", err)
	}
}

func TestRestoreFolders(t *testing.T) {
	got := restoreFolders(sampleRemote(), "B/pc/Pictures")
	if len(got) != 1 || got[0] != "sub" {
		t.Fatalf("got %v", got)
	}
}

func TestHistoryPersists(t *testing.T) {
	p := Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}
	r := newRunner(p, false)
	r.addRecent("hello")
	r.recordIssues([]string{"boom"})
	r.saveHistory()
	again := newRunner(p, false).Snapshot()
	if len(again.Recent) != 1 || len(again.Issues) != 1 || again.Issues[0].Message != "boom" {
		t.Fatalf("history not restored: %+v %+v", again.Recent, again.Issues)
	}
	os.WriteFile(p.history(), []byte("{broken"), 0o600)
	if s := newRunner(p, false).Snapshot(); len(s.Recent) != 0 {
		t.Fatal("broken history should start fresh")
	}
}

func TestPassInterval(t *testing.T) {
	if _, ok := passInterval(manualSync); ok {
		t.Fatal("manual should not schedule")
	}
	if d, ok := passInterval(instantSync); !ok || d != instantRemoteCheck {
		t.Fatalf("instant: %v %v", d, ok)
	}
	if d, _ := passInterval(15); d != 15*time.Minute {
		t.Fatalf("15: %v", d)
	}
	if d, _ := passInterval(0); d != defaultEveryMinutes*time.Minute {
		t.Fatalf("default: %v", d)
	}
}

func TestLocalFingerprintChanges(t *testing.T) {
	dir := t.TempDir()
	cfg := AppConfig{Folders: []string{dir}}
	a, _ := localFingerprint(cfg, t.TempDir())
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("1"), 0o644)
	b, n := localFingerprint(cfg, t.TempDir())
	if a == b || n != 1 {
		t.Fatal("fingerprint did not change after adding a file")
	}
	if again, _ := localFingerprint(cfg, t.TempDir()); b != again {
		t.Fatal("fingerprint not stable")
	}
}

func TestFreeNameStopsOnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locked")
	os.Mkdir(dir, 0o755)
	os.Chmod(dir, 0)
	defer os.Chmod(dir, 0o755)
	if _, err := os.Lstat(filepath.Join(dir, "x")); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Skip("permissions not enforced here")
	}
	if _, err := freeName(filepath.Join(dir, "a.txt")); err == nil {
		t.Fatal("expected error instead of looping")
	}
}

func TestResetAccountState(t *testing.T) {
	p := Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}
	keep := p.appConfig()
	files := []string{p.index(), p.state(), p.backupState(), p.sessions(), p.scope(), keep}
	for _, f := range files {
		os.WriteFile(f, []byte("{}"), 0o600)
	}
	if err := resetAccountState(p); err != nil {
		t.Fatal(err)
	}
	for _, f := range files[:5] {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s not removed", filepath.Base(f))
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("settings removed on sign out")
	}
	if err := resetAccountState(p); err != nil {
		t.Fatal("second reset should be a no-op")
	}
}

func TestPauseCancelsRunningPass(t *testing.T) {
	r := newRunner(Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}, false)
	ctx, cancel := context.WithCancel(context.Background())
	r.passStop = cancel
	if err := r.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("pausing must stop the running pass right away")
	}
	if !r.isPaused() {
		t.Fatal("not paused")
	}
}

func TestCancelUploads(t *testing.T) {
	p := Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}
	r := newRunner(p, false)
	if _, err := r.CancelUploads(); err == nil {
		t.Fatal("expected error when nothing uploads")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.passStop = cancel
	big := filepath.Join(p.Root, "SD-Backup")
	r.setPending([]string{big, big, filepath.Join(p.Root, "notes.txt")})
	tops, err := r.CancelUploads()
	if err != nil || len(tops) != 2 || ctx.Err() == nil {
		t.Fatalf("tops %v err %v stopped %v", tops, err, ctx.Err() != nil)
	}
	c, _ := loadAppConfig(p)
	if len(c.Exclude) != 2 || !newExcluder(c.Exclude).skip(filepath.Join(big, "a/b.txt"), "SD-Backup/a/b.txt") {
		t.Fatalf("exclude %v", c.Exclude)
	}
	if len(r.Snapshot().Pending) != 0 {
		t.Fatal("pending not cleared")
	}
}

func TestTokenRefreshOutlivesPassContext(t *testing.T) {
	refreshes := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			refreshes++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"new","token_type":"Bearer","expires_in":3600}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	cfg := &oauth2.Config{ClientID: "c", Endpoint: oauth2.Endpoint{TokenURL: api.URL + "/token"}}
	expired := &oauth2.Token{AccessToken: "old", RefreshToken: "r", Expiry: time.Now().Add(-time.Minute)}
	long := context.Background()
	pass, cancel := context.WithCancel(context.Background())
	client := oauth2.NewClient(context.WithValue(long, oauth2.HTTPClient, api.Client()), cfg.TokenSource(long, expired))
	cancel()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, api.URL+"/x", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("token refresh failed after a pass ended: %v", err)
	}
	resp.Body.Close()
	if refreshes != 1 || pass.Err() == nil {
		t.Fatalf("refreshes %d", refreshes)
	}
}
