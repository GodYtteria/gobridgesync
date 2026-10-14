package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
)

func TestDecide(t *testing.T) {
	b := &Entry{ID: "a", MD5: "m", Size: 1, MTime: 10}
	same := &LocalFile{Size: 1, MTime: 10}
	edited := &LocalFile{Size: 2, MTime: 20}
	r := &RemoteFile{ID: "a", MD5: "m"}
	rEdited := &RemoteFile{ID: "a", MD5: "n"}
	rReplaced := &RemoteFile{ID: "b", MD5: "m"}

	cases := []struct {
		name string
		b    *Entry
		l    *LocalFile
		r    *RemoteFile
		want Action
	}{
		{"new both", nil, same, r, Compare},
		{"new local", nil, same, nil, Upload},
		{"new remote", nil, nil, r, Download},
		{"nothing", nil, nil, nil, Noop},
		{"unchanged", b, same, r, Noop},
		{"local edit", b, edited, r, Upload},
		{"remote edit", b, same, rEdited, Download},
		{"remote replaced", b, same, rReplaced, Download},
		{"both edit", b, edited, rEdited, Compare},
		{"local delete", b, nil, r, DeleteRemote},
		{"local delete remote edit", b, nil, rEdited, Download},
		{"remote delete", b, same, nil, DeleteLocal},
		{"remote delete local edit", b, edited, nil, Upload},
		{"both delete", b, nil, nil, Forget},
	}
	for _, c := range cases {
		if got := decide(c.b, c.l, c.r); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestMassDelete(t *testing.T) {
	if massDelete(20, 21) || massDelete(30, 100) || !massDelete(30, 40) {
		t.Fatal("massDelete thresholds wrong")
	}
}

func TestBlocked(t *testing.T) {
	r := &Remote{Ambiguous: map[string]bool{"a/b": true}}
	if !r.blocked("a/b") || !r.blocked("a/b/c.txt") || r.blocked("a/bc") || r.blocked("a") {
		t.Fatal("blocked wrong")
	}
}

func TestValidNameFor(t *testing.T) {
	cases := []struct {
		goos, name string
		want       bool
	}{
		{"linux", "a:b?.txt", true},
		{"linux", "a/b", false},
		{"linux", "..", false},
		{"windows", "a:b.txt", false},
		{"windows", "con.txt", false},
		{"windows", "report.", false},
		{"windows", "console.txt", true},
		{"darwin", "a:b.txt", true},
	}
	for _, c := range cases {
		if got := validNameFor(c.goos, c.name); got != c.want {
			t.Errorf("%s %q: got %v want %v", c.goos, c.name, got, c.want)
		}
	}
}

func TestBackupNames(t *testing.T) {
	got := backupNames([]string{"/a/Docs", "/b/Docs", "/c/Pics"})
	want := []string{"Docs", "Docs-2", "Pics"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestNormalizeFolders(t *testing.T) {
	root := t.TempDir()
	a := root + "/a"
	sub := a + "/sub"
	sync := root + "/drive"
	for _, d := range []string{sub, sync} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := normalizeFolders([]string{a + "/"}, sync, nil); err != nil || len(got) != 1 || got[0] != a {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range [][]string{{"relative"}, {root + "/missing"}, {a, sub}, {sync}, {root}} {
		if _, err := normalizeFolders(bad, sync, nil); err == nil {
			t.Errorf("%v: expected error", bad)
		}
	}
}

func TestScanTreeSkips(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"keep.txt", "node_modules/x.js", ".git/config", "dir/b.gdoc"} {
		p := root + "/" + f
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	got, dirs, errs := scanTree(root, nil)
	if len(errs) != 0 || len(got) != 4 || len(dirs) != 3 {
		t.Fatalf("got %v dirs %v errs %v", got, dirs, errs)
	}
	if _, ok := got["dir/b.gdoc"]; !ok {
		t.Fatal("real .gdoc file should be backed up")
	}
	if _, ok := got["node_modules/x.js"]; !ok {
		t.Fatal("chosen folders are backed up in full; nothing is skipped automatically")
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KB", 15 << 30: "15.0 GB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("%d: got %q want %q", n, got, want)
		}
	}
}

func TestListDirs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"b", "A", ".hidden"} {
		os.Mkdir(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "file.txt"), nil, 0o644)
	got, err := listDirs(root)
	if err != nil || len(got.Dirs) != 2 || got.Dirs[0].Name != "A" || got.Dirs[1].Path != filepath.Join(root, "b") || got.Parent == "" {
		t.Fatalf("got %+v %v", got, err)
	}
	if _, err := listDirs("relative"); err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestTrayStatus(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		s     Status
		badge string
		line  string
	}{
		{Status{State: "signin", Message: "Sign in"}, "setup", "● Setup needed: Sign in"},
		{Status{State: "idle", Paused: true}, "paused", "⏸ Paused"},
		{Status{State: "syncing", Progress: Progress{Phase: "uploading", FilesDone: 1, FilesTotal: 4, BytesDone: 50, BytesTotal: 200}}, "sync", "⟳ Uploading 1/4 files (25%)"},
		{Status{State: "error", Issues: []Issue{{}, {}}}, "error", "⚠ 2 problem(s), open GoBridgeSync"},
		{Status{State: "idle", LastSync: now.Add(-5 * time.Minute)}, "ok", "✓ Up to date · 5m ago"},
	}
	for _, c := range cases {
		line, badge := trayStatus(c.s, now)
		if line != c.line || badge != c.badge {
			t.Errorf("got %q/%q want %q/%q", line, badge, c.line, c.badge)
		}
		if trayActive(badge) != (badge == "ok" || badge == "sync") {
			t.Errorf("trayActive(%q) wrong", badge)
		}
	}
}

func TestTrayFilesExist(t *testing.T) {
	for _, f := range []string{"color.png", "gray.png", "color.ico", "gray.ico", "mono-on.png", "mono-off.png", "template.png"} {
		if _, err := os.Stat("assets/tray/" + f); err != nil {
			t.Errorf("missing tray icon %s", f)
		}
	}
}

func TestAlerts(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var sent []string
	r := &Runner{notify: func(title, body string) error { sent = append(sent, body); return nil }}
	r.alert([]string{"disk full"}, nil, now)
	r.alert([]string{"disk full"}, nil, now.Add(time.Hour))
	r.alert([]string{"disk full"}, nil, now.Add(7*time.Hour))
	r.alert([]string{"a", "b"}, nil, now.Add(8*time.Hour))
	r.alert(nil, nil, now.Add(9*time.Hour))
	r.alert(nil, nil, now.Add(10*time.Hour))
	q := &Quota{Used: 95, Limit: 100}
	r.alert(nil, q, now.Add(11*time.Hour))
	r.alert(nil, q, now.Add(12*time.Hour))
	want := []string{"disk full", "disk full", "a (and 1 more)", "Backup is working again."}
	if len(sent) != 5 || sent[4] == "" {
		t.Fatalf("got %q", sent)
	}
	for i, w := range want {
		if sent[i] != w {
			t.Fatalf("got %q want prefix %q", sent, want)
		}
	}
	if quotaWarning(&Quota{Used: 89, Limit: 100}) != "" || quotaWarning(&Quota{Used: 5, Limit: 0}) != "" {
		t.Fatal("quota warning thresholds wrong")
	}
}

func TestMergeIssues(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	got := mergeIssues([]Issue{{t0, "a"}, {t0, "b"}}, []string{"a", "a", "c"}, t1)
	if len(got) != 3 || got[0] != (Issue{t1, "a"}) || got[1] != (Issue{t1, "c"}) || got[2] != (Issue{t0, "b"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestFitJobs(t *testing.T) {
	q := Quota{Used: 40, Limit: 100}
	jobs := []uploadJob{
		{rp: "a", lf: LocalFile{Size: 30}},
		{rp: "b", lf: LocalFile{Size: 20}},
		{rp: "c", lf: LocalFile{Size: 50}, r: &RemoteFile{Size: 45}},
		{rp: "d", lf: LocalFile{Size: 5}},
	}
	fit, skipped, sb := fitJobs(jobs, q, 90)
	if len(fit) != 2 || fit[0].rp != "a" || fit[1].rp != "b" || len(skipped) != 2 || sb != 55 {
		t.Fatalf("fit %+v skipped %+v bytes %d", fit, skipped, sb)
	}
	if fit, _, _ := fitJobs(jobs[2:], Quota{Used: 80, Limit: 100}, 90); len(fit) != 2 {
		t.Fatal("update should only cost the size difference")
	}
	if fit, _, _ := fitJobs(jobs, Quota{Used: 10}, 50); len(fit) != 4 {
		t.Fatal("unlimited quota should fit all")
	}
	if fit, skipped, _ := fitJobs(jobs, Quota{Used: 60, Limit: 100}, 50); len(fit) != 0 || len(skipped) != 4 {
		t.Fatal("over limit should skip all")
	}
	if (AppConfig{Limit: 70}).limit() != 90 || (AppConfig{}).limit() != 90 || (AppConfig{Limit: 75}).limit() != 75 || (AppConfig{Limit: 100}).limit() != 100 {
		t.Fatal("limit normalization wrong")
	}
}

func TestEmptyDirs(t *testing.T) {
	files := map[string]LocalFile{"a/x.txt": {}, "a/b/y.txt": {}}
	got := emptyDirs(files, []string{"a", "a/b", "c", "c/d", "e"})
	if len(got) != 2 || got[0] != "c/d" || got[1] != "e" {
		t.Fatalf("got %v", got)
	}
	if got := emptyDirs(map[string]LocalFile{}, nil); len(got) != 1 || got[0] != "." {
		t.Fatalf("empty root: got %v", got)
	}
}

func TestRetry(t *testing.T) {
	retryBase = time.Millisecond
	rate := &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{Reason: "userRateLimitExceeded"}}}
	calls := 0
	err := retry(context.Background(), func() error {
		if calls++; calls < 3 {
			return rate
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err %v calls %d", err, calls)
	}
	calls = 0
	denied := &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{Reason: "insufficientPermissions"}}}
	if err := retry(context.Background(), func() error { calls++; return denied }); err != denied || calls != 1 {
		t.Fatalf("non-retryable: err %v calls %d", err, calls)
	}
	calls = 0
	if err := retry(context.Background(), func() error { calls++; return &googleapi.Error{Code: 503} }); err == nil || calls != retryAttempts {
		t.Fatalf("exhausted: err %v calls %d", err, calls)
	}
}

func TestRemoteSub(t *testing.T) {
	r := &Remote{
		Files:     map[string]RemoteFile{"GoBridgeSync/a.txt": {ID: "1"}, "GoBridgeSync/d/b.txt": {ID: "2"}, "Other/c.txt": {ID: "3"}, "GoBridgeSyncX/e.txt": {ID: "4"}},
		Docs:      map[string]RemoteDoc{"GoBridgeSync/n.gdoc": {ID: "5"}},
		Folders:   map[string]string{"": "root", "GoBridgeSync": "g", "GoBridgeSync/d": "d"},
		Ambiguous: map[string]bool{foldKey("GoBridgeSync/dup"): true},
	}
	s := r.sub("GoBridgeSync", "g")
	if len(s.Files) != 2 || s.Files["a.txt"].ID != "1" || s.Files["d/b.txt"].ID != "2" {
		t.Fatalf("files %+v", s.Files)
	}
	if s.Folders[""] != "g" || s.Folders["d"] != "d" || len(s.Docs) != 1 || !s.blocked("dup/x") {
		t.Fatalf("sub %+v", s)
	}
}

func TestShortHome(t *testing.T) {
	sep := string(filepath.Separator)
	home := sep + "home" + sep + "a"
	if got := shortHome(home+sep+"Pictures", home); got != "~"+sep+"Pictures" {
		t.Fatalf("got %q", got)
	}
	if got := shortHome(home+"b"+sep+"x", home); got != home+"b"+sep+"x" {
		t.Fatalf("prefix match without separator: %q", got)
	}
}

func TestIntervalsAndStartup(t *testing.T) {
	if (AppConfig{}).every() != 5 || (AppConfig{Every: 7}).every() != 5 || (AppConfig{Every: manualSync}).every() != manualSync || (AppConfig{Every: 60}).every() != 60 {
		t.Fatal("interval normalization wrong")
	}
	off := false
	if !(AppConfig{}).startAtLogin() || (AppConfig{Startup: &off}).startAtLogin() {
		t.Fatal("start at login default wrong")
	}
}

func TestApplySettingsPartial(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep")
	os.Mkdir(keep, 0o755)
	cfg := AppConfig{Folders: []string{keep}, TwoWay: true, Limit: 90, Every: 5}
	every := 15
	got, err := applySettings(cfg, settingsInput{Every: &every}, filepath.Join(root, "sync"))
	if err != nil || len(got.Folders) != 1 || !got.TwoWay || got.Limit != 90 || got.Every != 15 {
		t.Fatalf("partial update changed other fields: %+v %v", got, err)
	}
	empty := []string{}
	if got, _ := applySettings(cfg, settingsInput{Folders: &empty}, filepath.Join(root, "sync")); len(got.Folders) != 0 {
		t.Fatal("explicit empty folder list should clear")
	}
	bad := 70
	if _, err := applySettings(cfg, settingsInput{Limit: &bad}, root); err == nil {
		t.Fatal("expected invalid limit error")
	}
}

func TestDefaultConfig(t *testing.T) {
	c := defaultAppConfig()
	if !c.TwoWay || c.limit() != 90 || c.every() != 5 || !c.startAtLogin() || c.Paused {
		t.Fatalf("defaults %+v", c)
	}
}

func TestFolderSuggestions(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"Documents", "Pictures", "Music"} {
		os.Mkdir(filepath.Join(home, d), 0o755)
	}
	got := folderSuggestions(home, []string{filepath.Join(home, "Pictures")}, filepath.Join(home, "GoBridgeSync"))
	if len(got) != 2 || filepath.Base(got[0]) != "Documents" || filepath.Base(got[1]) != "Music" {
		t.Fatalf("got %v", got)
	}
	if c := defaultAppConfig(); len(c.Folders) != 0 {
		t.Fatalf("new installs should not preselect folders: %v", c.Folders)
	}
}

func TestConfigMigration(t *testing.T) {
	old := AppConfig{Folders: []string{"/x"}, Exclude: []string{"*.tmp", ".git"}}
	got, changed := old.migrate()
	if !changed || got.Version != configVersion || len(got.Exclude) != 1+len(formerBuiltInSkips) {
		t.Fatalf("got %+v", got)
	}
	if _, again := got.migrate(); again {
		t.Fatal("migration ran twice")
	}
	if fresh, changed := (AppConfig{}).migrate(); !changed || len(fresh.Exclude) != 0 {
		t.Fatalf("no folders, nothing to keep: %+v", fresh)
	}
}

func TestMeasure(t *testing.T) {
	p := Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), make([]byte, 100), 0o644)
	os.WriteFile(filepath.Join(dir, "b"), make([]byte, 50), 0o644)
	r := newRunner(p, false)
	m, err := r.Measure(dir)
	if err != nil || m.Files != 2 || m.Bytes != 150 || m.Known || !m.Fits {
		t.Fatalf("got %+v %v", m, err)
	}
	r.set(func(s *Status) { s.Quota = &Quota{Used: 950, Limit: 1000} })
	m, _ = r.Measure(dir)
	if !m.Known || m.Fits || m.Free != 0 {
		t.Fatalf("with quota: %+v", m)
	}
	if _, err := r.Measure(p.Root); err == nil {
		t.Fatal("sync folder should be rejected before scanning")
	}
}

func TestNormalizeFoldersKeepsMissingKnown(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "unplugged")
	fresh := filepath.Join(root, "new")
	os.Mkdir(fresh, 0o755)
	got, err := normalizeFolders([]string{gone, fresh}, filepath.Join(root, "sync"), []string{gone})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := normalizeFolders([]string{gone}, filepath.Join(root, "sync"), nil); err == nil {
		t.Fatal("unknown missing folder must be rejected")
	}
}

func TestEditConfigConcurrent(t *testing.T) {
	p := Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			editConfig(p, func(c *AppConfig) error {
				c.Exclude = append(c.Exclude, "x")
				return nil
			})
		}()
	}
	wg.Wait()
	c, _ := loadAppConfig(p)
	if len(c.Exclude) != 20 {
		t.Fatalf("lost updates: %d of 20", len(c.Exclude))
	}
}

func TestEditConfigSkipsUnchangedWrite(t *testing.T) {
	p := Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}
	editConfig(p, func(c *AppConfig) error { c.Every = 15; return nil })
	past := time.Now().Add(-time.Hour)
	os.Chtimes(p.appConfig(), past, past)
	editConfig(p, func(*AppConfig) error { return nil })
	info, _ := os.Stat(p.appConfig())
	if !info.ModTime().Equal(past) {
		t.Fatal("unchanged config was rewritten")
	}
	editConfig(p, func(c *AppConfig) error { c.Every = 60; return nil })
	if c, _ := loadAppConfig(p); c.Every != 60 {
		t.Fatal("changed config not saved")
	}
}
