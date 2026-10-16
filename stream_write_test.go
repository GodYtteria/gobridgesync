package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

type wfile struct {
	ID, Name, MD5 string
	Parents       []string
	Trashed       bool
	MTime         string
	body          []byte
}

type wfake struct {
	mu        sync.Mutex
	files     map[string]*wfile
	next      int
	delay     time.Duration
	ups       int
	failPatch bool
	strict    bool
}

func (f *wfake) rejectsMeta(w http.ResponseWriter, meta map[string]any) bool {
	if !f.strict {
		return false
	}
	_, hasID := meta["id"]
	_, hasParents := meta["parents"]
	if !hasID && !hasParents {
		return false
	}
	http.Error(w, `{"error":{"code":403,"message":"fieldNotWritable"}}`, http.StatusForbidden)
	return true
}

func (f *wfake) put(id, name, parent, content string) *wfile {
	sum := md5.Sum([]byte(content))
	file := &wfile{ID: id, Name: name, MD5: hex.EncodeToString(sum[:]), Parents: []string{parent}, body: []byte(content)}
	f.files[id] = file
	return file
}

func (f *wfake) json(w http.ResponseWriter, file *wfile) {
	w.Header().Set("Content-Type", "application/json")
	mt := file.MTime
	if mt == "" {
		mt = "2026-01-01T00:00:00Z"
	}
	json.NewEncoder(w).Encode(map[string]any{"id": file.ID, "name": file.Name, "md5Checksum": file.MD5, "trashed": file.Trashed, "parents": file.Parents, "size": strconv.Itoa(len(file.body)), "modifiedTime": mt})
}

func (f *wfake) upload(r *http.Request) (map[string]any, []byte) {
	_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	mr := multipart.NewReader(r.Body, params["boundary"])
	p1, _ := mr.NextPart()
	var meta map[string]any
	json.NewDecoder(p1).Decode(&meta)
	p2, _ := mr.NextPart()
	body, _ := io.ReadAll(p2)
	return meta, body
}

func (f *wfake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	delay := f.delay
	f.mu.Unlock()
	if delay > 0 && strings.HasPrefix(r.URL.Path, "/upload") {
		time.Sleep(delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/upload/drive/v3")
	switch {
	case p == "/files/generateIds":
		ids := make([]string, 50)
		for i := range ids {
			f.next++
			ids[i] = fmt.Sprintf("gen%d", f.next)
		}
		json.NewEncoder(w).Encode(map[string]any{"ids": ids})
	case r.Method == http.MethodGet && p == "/files":
		var out []map[string]any
		for _, file := range f.files {
			if !file.Trashed && strings.Contains(r.URL.Query().Get("q"), "'"+file.Parents[0]+"'") {
				out = append(out, map[string]any{"id": file.ID})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"files": out})
	case r.Method == http.MethodGet:
		file := f.files[strings.TrimPrefix(p, "/files/")]
		if file == nil {
			http.Error(w, `{"error":{"code":404,"message":"nf"}}`, http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("alt") == "media" {
			var from, to int
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &from, &to)
			to = min(to, len(file.body)-1)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, len(file.body)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(file.body[from : to+1])
			return
		}
		f.json(w, file)
	case r.Method == http.MethodPost:
		if strings.HasPrefix(r.URL.Path, "/upload") {
			f.ups++
		}
		var meta map[string]any
		var body []byte
		if strings.HasPrefix(r.URL.Path, "/upload") {
			meta, body = f.upload(r)
		} else {
			json.NewDecoder(r.Body).Decode(&meta)
		}
		id, _ := meta["id"].(string)
		if _, exists := f.files[id]; exists {
			http.Error(w, `{"error":{"code":409,"message":"dup"}}`, http.StatusConflict)
			return
		}
		parent := "root"
		if ps, ok := meta["parents"].([]any); ok && len(ps) > 0 {
			parent = ps[0].(string)
		}
		name, _ := meta["name"].(string)
		created := f.put(id, name, parent, string(body))
		if mt, ok := meta["modifiedTime"].(string); ok {
			created.MTime = mt
		}
		f.json(w, created)
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/upload"):
		f.ups++
		meta, body := f.upload(r)
		if f.rejectsMeta(w, meta) {
			return
		}
		file := f.files[strings.TrimPrefix(p, "/files/")]
		sum := md5.Sum(body)
		file.body, file.MD5 = body, hex.EncodeToString(sum[:])
		if mt, ok := meta["modifiedTime"].(string); ok {
			file.MTime = mt
		}
		f.json(w, file)
	case r.Method == http.MethodPatch:
		if f.failPatch {
			http.Error(w, `{"error":{"code":403,"message":"nope"}}`, http.StatusForbidden)
			return
		}
		file := f.files[strings.TrimPrefix(p, "/files/")]
		var meta map[string]any
		json.NewDecoder(r.Body).Decode(&meta)
		if n, ok := meta["name"].(string); ok {
			file.Name = n
		}
		if t, ok := meta["trashed"].(bool); ok {
			file.Trashed = t
		}
		if mt, ok := meta["modifiedTime"].(string); ok {
			file.MTime = mt
		}
		if add := r.URL.Query().Get("addParents"); add != "" {
			file.Parents = []string{add}
		}
		f.json(w, file)
	}
}

func newWFakeServer(t *testing.T, fake *wfake) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(s.Close)
	return s
}

type wenv struct {
	rmu   sync.Mutex
	t     *testing.T
	fake  *wfake
	w     *streamWriter
	r     *Remote
	index *driveIndex
	dir   string
}

func newWEnv(t *testing.T) *wenv {
	return newWEnvWrap(t, nil)
}

func newWEnvWrap(t *testing.T, wrap func(fake *wfake) http.Handler) *wenv {
	fake := &wfake{files: map[string]*wfile{}}
	var h http.Handler = http.HandlerFunc(fake.serve)
	if wrap != nil {
		h = wrap(fake)
	}
	srvHTTP := httptest.NewServer(h)
	t.Cleanup(srvHTTP.Close)
	srv, _ := drive.NewService(context.Background(), option.WithHTTPClient(srvHTTP.Client()), option.WithEndpoint(srvHTTP.URL+"/"))
	cache, _ := newStreamCache(t.TempDir(), 1<<30)
	old := streamUploadDelay
	streamUploadDelay = 20 * time.Millisecond
	t.Cleanup(func() { streamUploadDelay = old })
	e := &wenv{t: t, fake: fake, dir: t.TempDir(), index: &driveIndex{RootID: "root", Files: map[string]*drive.File{"D": {Id: "D", Name: "docs", Parents: []string{"root"}, MimeType: folderMime}}}}
	e.r = &Remote{Folders: map[string]string{"": "root", "docs": "D"}, Files: map[string]RemoteFile{}}
	w, err := newStreamWriter(srv, e.dir, cache, e.snap, e.index, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.w = w
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w.Start(ctx, 1)
	return e
}

func (e *wenv) snap() *Remote {
	e.rmu.Lock()
	defer e.rmu.Unlock()
	return e.r.clone()
}

func (e *wenv) setR(r *Remote) {
	e.rmu.Lock()
	e.r = r
	e.rmu.Unlock()
}

func (e *wenv) addRemote(path, id, content string) {
	dir, name := splitPath(path)
	parent := "root"
	if dir != "" {
		parent = e.r.Folders[dir]
	}
	f := e.fake.put(id, name, parent, content)
	e.rmu.Lock()
	defer e.rmu.Unlock()
	e.r.Files[path] = RemoteFile{ID: id, MD5: f.MD5, Size: int64(len(content))}
}

func (e *wenv) file(id string) wfile {
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	if f := e.fake.files[id]; f != nil {
		return *f
	}
	return wfile{}
}

func (e *wenv) count() int {
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	return len(e.fake.files)
}

func (e *wenv) wait(cond func() bool) {
	deadline := time.Now().Add(5 * time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		e.t.Fatal("timed out")
	}
}

func (e *wenv) remoteByName(name string) *wfile {
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	for _, f := range e.fake.files {
		if f.Name == name && !f.Trashed {
			cp := *f
			return &cp
		}
	}
	return nil
}

func TestStreamWriteNewFileIsUploadedOnClose(t *testing.T) {
	e := newWEnv(t)
	sf, err := e.w.Create("docs/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	sf.WriteAt([]byte("hello world"), 0)
	if got := e.w.PendingIn("docs"); len(got) != 1 || got[0].File.Size != 11 {
		t.Fatalf("pending file must be visible with its size: %+v", got)
	}
	e.w.Release(sf, true)
	e.wait(func() bool { return e.remoteByName("new.txt") != nil && e.w.Pending() == 0 })
	got := e.file(e.remoteByName("new.txt").ID)
	if string(got.body) != "hello world" || got.Parents[0] != "D" {
		t.Fatalf("uploaded %+v", got)
	}
	e.index.mu.Lock()
	_, known := e.index.Files[got.ID]
	e.index.mu.Unlock()
	if !known {
		t.Fatal("the index must learn about the new file")
	}
	if _, err := e.w.Create("docs/new.txt"); err != nil && err != errStreamExists {
		t.Fatal(err)
	}
}

func TestStreamWriteEditsExistingFileInPlace(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "0123456789")
	sf, err := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 10)
	sf.ReadAt(buf, 0)
	if string(buf) != "0123456789" {
		t.Fatalf("the edit must start from the Drive content, got %q", buf)
	}
	sf.WriteAt([]byte("XY"), 3)
	e.w.Release(sf, true)
	e.wait(func() bool { return string(e.file("A1").body) == "012XY56789" && e.w.Pending() == 0 })
	if e.count() != 1 {
		t.Fatalf("an edit must update the file, not create another: %d files", e.count())
	}
}

func TestStreamWriteKeepsBothWhenDriveChangedMeanwhile(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "original")
	sf, _ := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	sf.WriteAt([]byte("mine"), 0)
	e.fake.mu.Lock()
	sum := md5.Sum([]byte("theirs"))
	e.fake.files["A1"].body, e.fake.files["A1"].MD5 = []byte("theirs"), hex.EncodeToString(sum[:])
	e.fake.mu.Unlock()
	e.w.Release(sf, true)
	e.wait(func() bool { return e.count() == 2 && e.w.Pending() == 0 })
	if string(e.file("A1").body) != "theirs" {
		t.Fatal("the other computer's edit must never be overwritten")
	}
	var conflict wfile
	e.fake.mu.Lock()
	for _, f := range e.fake.files {
		if f.ID != "A1" {
			conflict = *f
		}
	}
	e.fake.mu.Unlock()
	if !strings.Contains(conflict.Name, "(conflict") || string(conflict.body) != "mineinal" {
		t.Fatalf("conflict copy %+v", conflict)
	}
}

func TestStreamWriteRemoveChecksTheLiveFile(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "one")
	e.addRemote("docs/b.txt", "B1", "two")
	sum := md5.Sum([]byte("changed"))
	e.fake.mu.Lock()
	e.fake.files["B1"].MD5 = hex.EncodeToString(sum[:])
	e.fake.mu.Unlock()
	if err := e.w.Remove(context.Background(), "docs/b.txt"); err != errStreamStale || e.file("B1").Trashed {
		t.Fatalf("a file changed on Drive must not be deleted: %v", err)
	}
	if err := e.w.Remove(context.Background(), "docs/a.txt"); err != nil || !e.file("A1").Trashed {
		t.Fatalf("remove: %v", err)
	}
	e.index.mu.Lock()
	_, known := e.index.Files["A1"]
	e.index.mu.Unlock()
	if known {
		t.Fatal("index must forget a trashed file")
	}
}

func TestStreamWriteRenameReplacesAndMoves(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "one")
	e.addRemote("b.txt", "B1", "two")
	e.index.Files["A1"] = &drive.File{Id: "A1", Name: "a.txt", Parents: []string{"D"}}
	if err := e.w.Rename(context.Background(), "docs/a.txt", "b.txt"); err != nil {
		t.Fatal(err)
	}
	if a, b := e.file("A1"), e.file("B1"); !b.Trashed || a.Name != "b.txt" || a.Parents[0] != "root" {
		t.Fatalf("replace-by-rename: a=%+v b=%+v", a, b)
	}
}

func TestStreamWriteMkdirRmdirAndErrors(t *testing.T) {
	e := newWEnv(t)
	if err := e.w.Mkdir(context.Background(), "docs/sub"); err != nil {
		t.Fatal(err)
	}
	if err := e.w.Mkdir(context.Background(), "docs"); err != errStreamExists {
		t.Fatalf("mkdir over an existing folder: %v", err)
	}
	if err := e.w.Mkdir(context.Background(), "nope/x"); err != errStreamMissing {
		t.Fatalf("mkdir in a missing folder: %v", err)
	}
	if _, err := e.w.Create("nope/x.txt"); err != errStreamMissing {
		t.Fatalf("create in a missing folder: %v", err)
	}
	if _, err := e.w.Create("docs/bad\x00name"); err != errStreamBadName {
		t.Fatalf("bad name: %v", err)
	}
}

func TestStreamWriteSurvivesRestart(t *testing.T) {
	e := newWEnv(t)
	sf, _ := e.w.Create("docs/keep.txt")
	sf.WriteAt([]byte("precious"), 0)
	sf.mu.Lock()
	sf.opens, sf.writers = 0, 0
	sf.mu.Unlock()
	if err := e.w.saveMeta(sf); err != nil {
		t.Fatal(err)
	}
	sf.f.Close()
	cache, _ := newStreamCache(t.TempDir(), 1<<30)
	again, err := newStreamWriter(e.w.srv, e.dir, cache, e.snap, e.index, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	again.Start(ctx, 1)
	e.wait(func() bool { return e.remoteByName("keep.txt") != nil })
	if got := e.file(e.remoteByName("keep.txt").ID); string(got.body) != "precious" {
		t.Fatal("a file that was saved locally but not yet uploaded must upload after a restart")
	}
}

func TestStreamWriteNeverUploadsTheSameFileTwiceAtOnce(t *testing.T) {
	e := newWEnv(t)
	sf, _ := e.w.Create("docs/once.txt")
	sf.WriteAt([]byte("data"), 0)
	for i := 0; i < 5; i++ {
		e.w.enqueue(sf)
	}
	e.w.Release(sf, true)
	e.wait(func() bool { return e.remoteByName("once.txt") != nil && e.w.Pending() == 0 })
	time.Sleep(200 * time.Millisecond)
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	if len(e.fake.files) != 1 {
		t.Fatalf("one local file must become one Drive file, got %d (a duplicate or conflict copy appeared)", len(e.fake.files))
	}
}

func TestStreamWriteRenameDuringUploadLeavesOnlyTheNewName(t *testing.T) {
	e := newWEnv(t)
	sf, _ := e.w.Create("docs/old.txt")
	sf.WriteAt([]byte("data"), 0)
	e.w.Release(sf, true)
	if err := e.w.Rename(context.Background(), "docs/old.txt", "docs/new.txt"); err != nil {
		t.Fatal(err)
	}
	e.wait(func() bool { return e.w.Pending() == 0 })
	time.Sleep(200 * time.Millisecond)
	if e.remoteByName("old.txt") != nil || e.remoteByName("new.txt") == nil {
		t.Fatalf("after a rename the old name must not remain on Drive: old=%v new=%v", e.remoteByName("old.txt"), e.remoteByName("new.txt"))
	}
}

func TestStreamWriteListingWhileRenamingDoesNotDeadlock(t *testing.T) {
	e := newWEnv(t)
	sf, _ := e.w.Create("docs/busy.txt")
	sf.WriteAt([]byte("x"), 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			e.w.PendingIn("docs")
			e.w.PendingIn("")
		}
	}()
	for i := 0; i < 300; i++ {
		from, to := "docs/busy.txt", "docs/busy2.txt"
		if i%2 == 1 {
			from, to = to, from
		}
		if err := e.w.Rename(context.Background(), from, to); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("listing and renaming a pending file deadlocked")
	}
}

func TestStreamWriteRenameOverExistingKeepsTheDriveFile(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/doc.txt", "DOC1", "old text")
	streamUploadDelay = 400 * time.Millisecond
	sf, err := e.w.Create("docs/doc.txt.tmp")
	if err != nil {
		t.Fatal(err)
	}
	sf.WriteAt([]byte("new text"), 0)
	e.w.Release(sf, true)
	if err := e.w.Rename(context.Background(), "docs/doc.txt.tmp", "docs/doc.txt"); err != nil {
		t.Fatal(err)
	}
	e.wait(func() bool { return string(e.file("DOC1").body) == "new text" && e.w.Pending() == 0 })
	if e.count() != 1 || e.file("DOC1").Trashed {
		t.Fatalf("an editor's save-by-rename must update the same Drive file (links, comments, history), got %d files", e.count())
	}
}

func TestStreamWriteFolderRenameMovesPendingChildren(t *testing.T) {
	e := newWEnv(t)
	e.fake.mu.Lock()
	e.fake.delay = 300 * time.Millisecond
	e.fake.mu.Unlock()
	e.fake.put("D", "docs", "root", "")
	e.index.Files["D"] = &drive.File{Id: "D", Name: "docs", Parents: []string{"root"}, MimeType: folderMime}
	streamUploadDelay = time.Hour
	sf, _ := e.w.Create("docs/inside.txt")
	sf.WriteAt([]byte("child"), 0)
	e.w.Release(sf, true)
	if err := e.w.Rename(context.Background(), "docs", "docs2"); err != nil {
		t.Fatal(err)
	}
	if got := e.w.PendingIn("docs2"); len(got) != 1 {
		t.Fatalf("a pending file must follow its folder, got %+v", got)
	}
	e.rmu.Lock()
	e.r.Folders = map[string]string{"": "root", "docs2": "D"}
	e.rmu.Unlock()
	e.w.enqueue(sf)
	e.wait(func() bool { return e.remoteByName("inside.txt") != nil && e.w.Pending() == 0 })
	if got := e.remoteByName("inside.txt"); got.Parents[0] != "D" {
		t.Fatalf("the file must land in the renamed folder: %+v", got)
	}
}

func TestStreamWriteStageStillOpenAtCrashIsSavedAsRecovered(t *testing.T) {
	e := newWEnv(t)
	sf, _ := e.w.Create("docs/draft.txt")
	sf.WriteAt([]byte("half written"), 0)
	sf.f.Close()
	cache, _ := newStreamCache(t.TempDir(), 1<<30)
	again, err := newStreamWriter(e.w.srv, e.dir, cache, e.snap, e.index, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	again.Start(ctx, 1)
	e.wait(func() bool {
		e.fake.mu.Lock()
		defer e.fake.mu.Unlock()
		for _, f := range e.fake.files {
			if strings.HasPrefix(f.Name, "draft (recovered") && string(f.body) == "half written" {
				return true
			}
		}
		return false
	})
	if e.remoteByName("draft.txt") != nil {
		t.Fatal("an unfinished file must never be saved under the real name")
	}
}

func TestStreamWriteDeleteDuringUploadLeavesNothingBehind(t *testing.T) {
	e := newWEnv(t)
	e.fake.mu.Lock()
	e.fake.delay = 400 * time.Millisecond
	e.fake.mu.Unlock()
	sf, _ := e.w.Create("docs/ghost.txt")
	sf.WriteAt([]byte("temp"), 0)
	e.w.Release(sf, true)
	time.Sleep(100 * time.Millisecond)
	if err := e.w.Remove(context.Background(), "docs/ghost.txt"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	if e.remoteByName("ghost.txt") != nil {
		t.Fatal("a file deleted while it was being saved must not reappear on Drive")
	}
}

func TestStreamWriteReopenAfterCompletionStartsAFreshStage(t *testing.T) {
	e := newWEnv(t)
	sf, _ := e.w.Create("docs/again.txt")
	sf.WriteAt([]byte("one"), 0)
	e.w.Release(sf, true)
	e.wait(func() bool { return e.remoteByName("again.txt") != nil && e.w.Pending() == 0 })
	if got := e.w.acquire("docs/again.txt", true); got != nil {
		t.Fatal("a finished upload must not hand out its closed stage again")
	}
}

func (e *wenv) uploads() int {
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	return e.fake.ups
}

func TestStreamWriteReopenForTruncateThenCrashNeverEmptiesTheRealFile(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "precious content")
	e.fake.mu.Lock()
	e.fake.delay = 600 * time.Millisecond
	e.fake.mu.Unlock()
	sf, _ := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	sf.WriteAt([]byte("edit"), 0)
	e.w.Release(sf, true)
	again, err := e.w.OpenWrite(context.Background(), "docs/a.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	if again != sf {
		t.Skip("the first edit finished uploading before the reopen")
	}
	again.Truncate(0)
	again.f.Close()
	cache, _ := newStreamCache(t.TempDir(), 1<<30)
	reborn, err := newStreamWriter(e.w.srv, e.dir, cache, e.snap, e.index, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, sf := range reborn.files {
		if sf.path == "docs/a.txt" {
			t.Fatalf("a stage that was open for writing when the app died must not be restored under its real name: %+v", sf.path)
		}
	}
}

func TestStreamWriteRenameOfAnEditedFileBeforeUploadIsAppliedOnDrive(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "text")
	sf, _ := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	sf.WriteAt([]byte("X"), 0)
	if err := e.w.Rename(context.Background(), "docs/a.txt", "docs/b.txt"); err != nil {
		t.Fatal(err)
	}
	e.w.Release(sf, true)
	e.wait(func() bool { return e.w.Pending() == 0 && e.file("A1").Name == "b.txt" })
	if string(e.file("A1").body) != "Xext" || e.count() != 1 {
		t.Fatalf("edit and rename must both land on the same Drive file: %+v (%d files)", e.file("A1"), e.count())
	}
}

func TestStreamWriteEditedFileMovedOverAnExistingOneReplacesIt(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "mine")
	e.addRemote("docs/b.txt", "B1", "theirs")
	sf, _ := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	sf.WriteAt([]byte("M"), 0)
	if err := e.w.Rename(context.Background(), "docs/a.txt", "docs/b.txt"); err != nil {
		t.Fatal(err)
	}
	e.w.Release(sf, true)
	e.wait(func() bool { return e.w.Pending() == 0 && e.file("A1").Name == "b.txt" && e.file("B1").Trashed })
	if e.remoteByName("a.txt") != nil {
		t.Fatal("the old name must be gone after a move")
	}
}

func TestStreamWriteMkdirRmdirMkdirWorksAgain(t *testing.T) {
	e := newWEnv(t)
	for i := 0; i < 3; i++ {
		if err := e.w.Mkdir(context.Background(), "docs/sub"); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		r, err := e.index.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		e.setR(r)
		if err := e.w.Rmdir(context.Background(), "docs/sub"); err != nil {
			t.Fatalf("round %d rmdir: %v", i, err)
		}
		r, _ = e.index.snapshot()
		e.setR(r)
	}
}

func TestStreamWriteDoesNotReuploadIdenticalContent(t *testing.T) {
	e := newWEnv(t)
	sf, _ := e.w.Create("docs/same.txt")
	sf.WriteAt([]byte("data"), 0)
	e.w.Release(sf, true)
	e.wait(func() bool { return e.remoteByName("same.txt") != nil && e.w.Pending() == 0 })
	first := e.uploads()
	got, err := e.w.OpenWrite(context.Background(), "docs/same.txt", false)
	if err != nil {
		t.Skip("snapshot does not list the file yet")
	}
	e.w.Release(got, true)
	time.Sleep(300 * time.Millisecond)
	if e.uploads() != first {
		t.Fatalf("opening and closing without changes must not upload again (%d -> %d)", first, e.uploads())
	}
}

func TestStreamWriteFailedRenameKeepsThePendingTargetsBytes(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "one")
	e.addRemote("docs/b.txt", "B1", "two")
	e.fake.mu.Lock()
	e.fake.failPatch = true
	e.fake.mu.Unlock()
	streamUploadDelay = time.Hour
	b, err := e.w.OpenWrite(context.Background(), "docs/b.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	b.WriteAt([]byte("B-EDIT"), 0)
	e.w.Release(b, true)
	if err := e.w.Rename(context.Background(), "docs/a.txt", "docs/b.txt"); err == nil {
		t.Fatal("the Drive rename was refused, so the rename must fail")
	}
	if e.w.lookupPending("docs/b.txt") == nil {
		t.Fatal("a failed rename must not throw away what the user saved into the target")
	}
}

func TestStreamWriteReaderDoesNotBlockTheUpload(t *testing.T) {
	e := newWEnv(t)
	sf, _ := e.w.Create("docs/seen.txt")
	sf.WriteAt([]byte("body"), 0)
	reader := e.w.acquire("docs/seen.txt", false)
	e.w.Release(sf, true)
	e.wait(func() bool { return e.remoteByName("seen.txt") != nil })
	e.w.Release(reader, false)
	e.wait(func() bool { return e.w.Pending() == 0 })
}

func TestStreamWriteRenameOverExistingWhileUploadIsRunningLeavesOneFile(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/real.txt", "R1", "old")
	e.fake.mu.Lock()
	e.fake.delay = 400 * time.Millisecond
	e.fake.mu.Unlock()
	sf, _ := e.w.Create("docs/tmp.txt")
	sf.WriteAt([]byte("new"), 0)
	e.w.Release(sf, true)
	e.wait(func() bool {
		sf.mu.Lock()
		defer sf.mu.Unlock()
		return sf.running
	})
	if err := e.w.Rename(context.Background(), "docs/tmp.txt", "docs/real.txt"); err != nil {
		t.Fatal(err)
	}
	e.wait(func() bool { return e.w.Pending() == 0 })
	time.Sleep(300 * time.Millisecond)
	live := 0
	e.fake.mu.Lock()
	for _, f := range e.fake.files {
		if f.Name == "real.txt" && !f.Trashed {
			live++
			if string(f.body) != "new" {
				t.Errorf("the live file must hold the new content: %q", f.body)
			}
		}
	}
	e.fake.mu.Unlock()
	if live != 1 {
		t.Fatalf("exactly one real.txt must remain, got %d", live)
	}
}

func TestStreamWriteReadAfterRestartDoesNotDiscardAnUnsentEdit(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "old")
	streamUploadDelay = time.Hour
	sf, _ := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	sf.WriteAt([]byte("NEW"), 0)
	e.w.Release(sf, true)
	sf.f.Close()
	streamUploadDelay = 20 * time.Millisecond
	cache, _ := newStreamCache(t.TempDir(), 1<<30)
	again, err := newStreamWriter(e.w.srv, e.dir, cache, e.snap, e.index, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.acquire("docs/a.txt", false); got != nil {
		again.Release(got, false)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	again.Start(ctx, 1)
	e.wait(func() bool { return string(e.file("A1").body) == "NEW" })
}

func TestStreamWriteReopenAbortsRunningUploadAndSavesFinalContent(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "0123456789")
	e.fake.mu.Lock()
	e.fake.delay = 400 * time.Millisecond
	e.fake.mu.Unlock()
	sf, _ := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	sf.WriteAt([]byte("first"), 0)
	e.w.Release(sf, true)
	e.wait(func() bool {
		sf.mu.Lock()
		defer sf.mu.Unlock()
		return sf.running
	})
	again, err := e.w.OpenWrite(context.Background(), "docs/a.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	sf.mu.Lock()
	cancelled := sf.cancel == nil || sf.again
	sf.mu.Unlock()
	if !cancelled {
		t.Fatal("reopening for write must abort the running upload")
	}
	again.WriteAt([]byte("FINAL"), 0)
	e.w.Release(again, true)
	e.wait(func() bool { return string(e.file("A1").body) == "FINAL" && e.w.Pending() == 0 })
	if e.count() != 1 {
		t.Fatalf("no extra files expected, got %d", e.count())
	}
}

func TestStreamWriteUnchangedOpenStageAtCrashLeavesNoCopy(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "0123456789")
	sf, err := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	sf.f.Close()
	cache, _ := newStreamCache(t.TempDir(), 1<<30)
	again, err := newStreamWriter(e.w.srv, e.dir, cache, e.snap, e.index, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	again.Start(ctx, 1)
	time.Sleep(600 * time.Millisecond)
	if e.count() != 1 || again.Pending() != 0 {
		t.Fatalf("an open file nobody wrote to must not leave a recovered copy: %d files, %d pending", e.count(), again.Pending())
	}
}

func TestUploadRetryDelayBacksOffAndCaps(t *testing.T) {
	if got := uploadRetryDelay(1); got != streamRetryUpload {
		t.Fatalf("first retry must wait the base delay, got %v", got)
	}
	if got := uploadRetryDelay(3); got != 4*streamRetryUpload {
		t.Fatalf("third retry must wait four times the base delay, got %v", got)
	}
	if got := uploadRetryDelay(50); got != streamRetryUploadMax {
		t.Fatalf("the delay must stop growing at the cap, got %v", got)
	}
}

func TestStreamWriteOrphanDetailsFileIsRemovedOnRestart(t *testing.T) {
	e := newWEnv(t)
	sf, _ := e.w.Create("docs/gone.txt")
	sf.WriteAt([]byte("x"), 0)
	if err := e.w.saveMeta(sf); err != nil {
		t.Fatal(err)
	}
	meta := sf.stage + ".json"
	sf.f.Close()
	os.Remove(sf.stage)
	cache, _ := newStreamCache(t.TempDir(), 1<<30)
	if _, err := newStreamWriter(e.w.srv, e.dir, cache, e.snap, e.index, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(meta); !os.IsNotExist(err) {
		t.Fatal("details whose staging file is gone must be removed, not kept forever")
	}
}

func TestStreamWriteReopenForWritingResetsUploadBackoff(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "0123456789")
	sf, err := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	sf.WriteAt([]byte("XY"), 0)
	sf.addFailure()
	sf.addFailure()
	e.w.Release(sf, true)
	again, err := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	sf.mu.Lock()
	n := sf.failures
	sf.mu.Unlock()
	e.w.Release(again, true)
	if n != 0 {
		t.Fatalf("a fresh edit must start with a clean retry delay, failures=%d", n)
	}
}
