package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func TestNormalizeExcludes(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := normalizeExcludes([]string{" *.tmp ", "node_modules", "~/Documents/SD-Backup/", "proj/build/", "*.tmp", ""})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"*.tmp", "node_modules", filepath.Join(home, "Documents", "SD-Backup"), "proj/build"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	for _, bad := range []string{"[", "..", "../x"} {
		if _, err := normalizeExcludes([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestExcluderSkip(t *testing.T) {
	x := newExcluder([]string{"*.tmp", "cache", "/data/Docs/SD-Backup", "proj/build"})
	cases := map[[2]string]bool{
		{"/data/Docs/a.tmp", "a.tmp"}:                       true,
		{"/data/Docs/x/cache", "x/cache"}:                   true,
		{"/data/Docs/SD-Backup", "SD-Backup"}:               true,
		{"/data/Docs/SD-Backup/f.jpg", "SD-Backup/f.jpg"}:   true,
		{"/data/Docs/SD-Backup2/f.jpg", "SD-Backup2/f.jpg"}: false,
		{"/data/Docs/proj/build/o.bin", "proj/build/o.bin"}: true,
		{"/data/Docs/proj/src/main.go", "proj/src/main.go"}: false,
		{"/data/Docs/notes.txt", "notes.txt"}:               false,
	}
	for in, want := range cases {
		if got := x.skip(filepath.FromSlash(in[0]), in[1]); got != want {
			t.Errorf("skip(%q) = %v", in[0], got)
		}
	}
	if (*excluder)(nil).skip("/a", "a") {
		t.Fatal("nil excluder skipped")
	}
}

func TestScanTreeExcludes(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"keep.txt", "a.tmp", "SD-Backup/big.jpg", "sub/cache/x"} {
		p := filepath.Join(root, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	files, _, _ := scanTree(root, newExcluder([]string{"*.tmp", "cache", filepath.Join(root, "SD-Backup")}))
	if len(files) != 1 {
		t.Fatalf("got %v", files)
	}
}

func TestSyncIgnoresExcludedPaths(t *testing.T) {
	x := newExcluder([]string{"*.tmp"})
	s := &Syncer{paths: Paths{Root: "/r"}, exclude: x}
	if !s.exclude.skip(s.abs("a/b.tmp"), "a/b.tmp") || s.exclude.skip(s.abs("a/b.txt"), "a/b.txt") {
		t.Fatal("sync exclusion check wrong")
	}
}

func TestDiskBudget(t *testing.T) {
	free, err := freeSpace(t.TempDir())
	if err != nil || free <= 0 {
		t.Fatalf("freeSpace: %d %v", free, err)
	}
	if b := diskBudget(t.TempDir()); b != max(free-diskReserve, 0) && b > free {
		t.Fatalf("budget %d free %d", b, free)
	}
	if diskBudget("/definitely/missing/path") <= 0 {
		t.Fatal("unknown free space should not block downloads")
	}
}

func TestSyncNeverDeletesNewlyExcludedFiles(t *testing.T) {
	x := newExcluder([]string{".git"})
	s := &Syncer{paths: Paths{Root: "/r"}, exclude: x}
	p := "proj/.git/objects/51/abc"
	if !s.exclude.skip(s.abs(p), p) {
		t.Fatal("file inside an excluded folder must be left alone by sync")
	}
}

func TestPrioritizeNewestFirstAndCap(t *testing.T) {
	more := 0
	s := &Syncer{more: func() { more++ }}
	local := map[string]LocalFile{}
	var jobs []pendingUpload
	for i := 0; i < uploadsPerPass+50; i++ {
		p := fmt.Sprintf("f%d", i)
		local[p] = LocalFile{MTime: int64(i)}
		jobs = append(jobs, pendingUpload{path: p})
	}
	got := s.prioritize(jobs, local)
	if len(got) != uploadsPerPass || got[0].path != fmt.Sprintf("f%d", uploadsPerPass+49) || more != 1 {
		t.Fatalf("len %d first %s more %d", len(got), got[0].path, more)
	}
}

func TestStillWriting(t *testing.T) {
	now := time.Now()
	if !stillWriting(LocalFile{Size: busyFileSize, MTime: now.UnixNano()}, now) {
		t.Fatal("large file being written should wait")
	}
	if stillWriting(LocalFile{Size: 1024, MTime: now.UnixNano()}, now) {
		t.Fatal("small files upload right away")
	}
	if stillWriting(LocalFile{Size: busyFileSize, MTime: now.Add(-3 * time.Minute).UnixNano()}, now) {
		t.Fatal("quiet large file should upload")
	}
}

func TestWaitForQuiet(t *testing.T) {
	now := time.Now()
	busy := LocalFile{Size: busyFileSize, MTime: now.UnixNano()}
	s := &Syncer{st: &State{Files: map[string]Entry{
		"recent.jsonl": {MTime: now.Add(-5 * time.Minute).UnixNano()},
		"stale.jsonl":  {MTime: now.Add(-20 * time.Minute).UnixNano()},
	}}}
	if !s.waitForQuiet("recent.jsonl", busy, now) {
		t.Fatal("busy file uploaded 5 minutes ago should wait")
	}
	if s.waitForQuiet("stale.jsonl", busy, now) {
		t.Fatal("busy file not uploaded for 20 minutes should upload")
	}
	if !s.waitForQuiet("new.jsonl", busy, now) {
		t.Fatal("new file still being written should wait until quiet")
	}
	if s.waitForQuiet("recent.jsonl", LocalFile{Size: 1024, MTime: now.UnixNano()}, now) {
		t.Fatal("small file should not wait")
	}
}

func TestDeleteRemoteAll(t *testing.T) {
	var mu sync.Mutex
	trashed := map[string]bool{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)
		if id == "gone" {
			http.Error(w, `{"error":{"code":404,"message":"not found"}}`, http.StatusNotFound)
			return
		}
		if id == "fail" {
			http.Error(w, `{"error":{"code":403,"message":"forbidden"}}`, http.StatusForbidden)
			return
		}
		mu.Lock()
		trashed[id] = true
		mu.Unlock()
		w.Write([]byte(`{"id":"` + id + `"}`))
	}))
	defer api.Close()
	srv, _ := drive.NewService(context.Background(), option.WithHTTPClient(api.Client()), option.WithEndpoint(api.URL+"/"))
	st := &State{Files: map[string]Entry{}}
	var paths []string
	for i := range 30 {
		p := fmt.Sprintf("f%d", i)
		st.Files[p] = Entry{ID: fmt.Sprintf("id%d", i)}
		paths = append(paths, p)
	}
	st.Files["g"], st.Files["x"] = Entry{ID: "gone"}, Entry{ID: "fail"}
	paths = append(paths, "g", "x")
	s := &Syncer{srv: srv, st: st}
	errs := s.deleteRemoteAll(context.Background(), paths)
	if len(errs) != 1 || len(trashed) != 30 {
		t.Fatalf("errs=%v trashed=%d", errs, len(trashed))
	}
	if _, ok := st.Files["x"]; !ok || len(st.Files) != 1 {
		t.Fatalf("state after deletes: %v", st.Files)
	}
}

func TestDeleteRemoteAllStopsWhenPaused(t *testing.T) {
	s := &Syncer{st: &State{Files: map[string]Entry{"a": {ID: "a"}}}, stop: func() bool { return true }}
	errs := s.deleteRemoteAll(context.Background(), []string{"a"})
	if len(errs) != 1 || !errors.Is(errs[0], errPaused) || len(s.st.Files) != 1 {
		t.Fatalf("errs=%v files=%v", errs, s.st.Files)
	}
}

func TestEmptyFolders(t *testing.T) {
	remote := &Remote{
		Files:   map[string]RemoteFile{"a/b/x.txt": {}, "a/b/y.txt": {}, "a/keep.txt": {}, "c/z.txt": {}, "d/e/w.txt": {}, "f/g.txt": {}},
		Docs:    map[string]RemoteDoc{"f/doc.gdoc": {}},
		Folders: map[string]string{"a": "A", "a/b": "AB", "c": "C", "d": "D", "d/e": "DE", "d/empty": "DX", "f": "F"},
	}
	deleted := map[string]bool{"a/b/x.txt": true, "a/b/y.txt": true, "c/z.txt": true, "d/e/w.txt": true, "f/g.txt": true}
	got := emptyFolders(remote, deleted, func(d string) bool { return d == "c" })
	want := []string{"a/b", "d/e"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	remote.Folders["d/empty"] = "DX"
	delete(remote.Folders, "d/empty")
	if got := emptyFolders(remote, deleted, func(string) bool { return false }); fmt.Sprint(got) != "[a/b c d]" {
		t.Fatalf("got %v", got)
	}
}

func TestTrashEmptyFoldersChecksDriveFirst(t *testing.T) {
	var mu sync.Mutex
	var trashed []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			q := r.URL.Query().Get("q")
			switch {
			case strings.Contains(q, "'FULL'"):
				w.Write([]byte(`{"files":[{"id":"SUB","mimeType":"application/vnd.google-apps.folder"}]}`))
			case strings.Contains(q, "'SUB'"):
				w.Write([]byte(`{"files":[{"id":"form","mimeType":"application/vnd.google-apps.form"}]}`))
			case strings.Contains(q, "'EMPTY'"):
				w.Write([]byte(`{"files":[{"id":"ida","mimeType":"text/plain"},{"id":"ESUB","mimeType":"application/vnd.google-apps.folder"}]}`))
			default:
				w.Write([]byte(`{"files":[]}`))
			}
			return
		}
		mu.Lock()
		trashed = append(trashed, path.Base(r.URL.Path))
		mu.Unlock()
		w.Write([]byte(`{"id":"x"}`))
	}))
	defer api.Close()
	srv, _ := drive.NewService(context.Background(), option.WithHTTPClient(api.Client()), option.WithEndpoint(api.URL+"/"))
	root := t.TempDir()
	s := &Syncer{srv: srv, paths: Paths{Root: root}, st: &State{Files: map[string]Entry{}}}
	remote := &Remote{
		Files:   map[string]RemoteFile{"empty/a.txt": {ID: "ida"}, "full/b.txt": {ID: "idb"}},
		Folders: map[string]string{"empty": "EMPTY", "full": "FULL"},
	}
	if errs := s.trashEmptyFolders(context.Background(), remote, []string{"empty/a.txt", "full/b.txt"}); len(errs) != 0 {
		t.Fatal(errs)
	}
	if fmt.Sprint(trashed) != "[EMPTY]" {
		t.Fatalf("trashed %v; a folder Drive still lists items in must be kept", trashed)
	}
}

func TestDriveFolderEmptyBatchesByLevel(t *testing.T) {
	var queries []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		queries = append(queries, q)
		if strings.Contains(q, "'TOP'") {
			w.Write([]byte(`{"files":[{"id":"S1","mimeType":"application/vnd.google-apps.folder"},{"id":"S2","mimeType":"application/vnd.google-apps.folder"},{"id":"S3","mimeType":"application/vnd.google-apps.folder"}]}`))
			return
		}
		w.Write([]byte(`{"files":[]}`))
	}))
	defer api.Close()
	srv, _ := drive.NewService(context.Background(), option.WithHTTPClient(api.Client()), option.WithEndpoint(api.URL+"/"))
	s := &Syncer{srv: srv}
	empty, err := s.driveFolderEmpty(context.Background(), "TOP", nil)
	if err != nil || !empty {
		t.Fatal(empty, err)
	}
	if len(queries) != 2 || !strings.Contains(queries[1], "'S1' in parents or 'S2' in parents or 'S3' in parents") {
		t.Fatalf("queries %q", queries)
	}
}

func TestDeleteRemoteAllRetriesRateLimit(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			http.Error(w, `{"error":{"code":429,"message":"slow down"}}`, http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"id":"a"}`))
	}))
	defer api.Close()
	srv, _ := drive.NewService(context.Background(), option.WithHTTPClient(api.Client()), option.WithEndpoint(api.URL+"/"))
	s := &Syncer{srv: srv, st: &State{Files: map[string]Entry{"a": {ID: "a"}}}}
	if errs := s.deleteRemoteAll(context.Background(), []string{"a"}); len(errs) != 0 || calls != 2 || len(s.st.Files) != 0 {
		t.Fatalf("errs=%v calls=%d files=%v", errs, calls, s.st.Files)
	}
}

func TestVanished(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.tmp")
	_, err := os.Open(gone)
	if !vanished(fmt.Errorf("upload: %w", err), gone) {
		t.Fatal("missing file should count as vanished")
	}
	here := filepath.Join(dir, "here.txt")
	os.WriteFile(here, []byte("x"), 0o644)
	if vanished(err, here) {
		t.Fatal("file that still exists is not vanished")
	}
	if vanished(errors.New("boom"), gone) {
		t.Fatal("other errors are not vanished")
	}
}
