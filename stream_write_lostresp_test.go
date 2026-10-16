package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func restartWriter(t *testing.T, e *wenv, journal *idJournal) *streamWriter {
	t.Helper()
	cache, _ := testCache(t, 1<<30)
	again, err := newStreamWriter(e.w.srv, e.dir, cache, e.snap, e.index, journal, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	again.Start(ctx, 1)
	return again
}

func writeStage(t *testing.T, dir, name, body string, meta map[string]any) string {
	t.Helper()
	stage := filepath.Join(dir, name)
	if err := os.WriteFile(stage, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(stage+".json", b, 0o600); err != nil {
		t.Fatal(err)
	}
	return stage
}

func commitThenSleep(method string, d time.Duration, committed chan<- struct{}) func(fake *wfake) http.Handler {
	var mu sync.Mutex
	armed := true
	return func(fake *wfake) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fake.serve(w, r)
			if r.Method != method || !strings.HasPrefix(r.URL.Path, "/upload") {
				return
			}
			mu.Lock()
			fire := armed
			armed = false
			mu.Unlock()
			if !fire {
				return
			}
			select {
			case committed <- struct{}{}:
			default:
			}
			time.Sleep(d)
		})
	}
}

func TestStreamWriteReopenAfterCommittedUpdateMakesNoConflictCopy(t *testing.T) {
	committed := make(chan struct{}, 1)
	e := newWEnvWrap(t, commitThenSleep(http.MethodPatch, 600*time.Millisecond, committed))
	e.addRemote("docs/a.txt", "A1", "old")
	sf, err := e.w.OpenWrite(context.Background(), "docs/a.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	sf.WriteAt([]byte("v1"), 0)
	e.w.Release(sf, true)
	<-committed
	again, err := e.w.OpenWrite(context.Background(), "docs/a.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	again.WriteAt([]byte("v2"), 0)
	e.w.Release(again, true)
	e.wait(func() bool { return e.w.Pending() == 0 })
	if e.count() != 1 || string(e.file("A1").body) != "v2" {
		t.Fatalf("expected one file with the final content, got %d files, A1=%q", e.count(), e.file("A1").body)
	}
}

func TestStreamWriteRestartAfterCommittedUpdateMakesNoConflictCopy(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "old")
	oldSum := e.file("A1").MD5
	e.fake.mu.Lock()
	e.fake.files["A1"].body = []byte("new")
	e.fake.files["A1"].MD5 = md5Hex("new")
	e.fake.mu.Unlock()
	writeStage(t, e.dir, "123-1", "new", map[string]any{"path": "docs/a.txt", "baseId": "A1", "baseMd5": oldSum})
	again := restartWriter(t, e, nil)
	e.wait(func() bool { return again.Pending() == 0 })
	if e.count() != 1 || string(e.file("A1").body) != "new" {
		t.Fatalf("content already on Drive must not make a conflict copy, got %d files", e.count())
	}
}

func TestStreamWriteRestartWithNewerContentAfterCommittedUpdateUpdatesInPlace(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "old")
	oldSum := e.file("A1").MD5
	e.fake.mu.Lock()
	e.fake.files["A1"].body = []byte("sent")
	e.fake.files["A1"].MD5 = md5Hex("sent")
	e.fake.mu.Unlock()
	writeStage(t, e.dir, "123-1", "newest", map[string]any{"path": "docs/a.txt", "baseId": "A1", "baseMd5": oldSum, "sentMd5": md5Hex("sent")})
	again := restartWriter(t, e, nil)
	e.wait(func() bool { return again.Pending() == 0 })
	if e.count() != 1 || string(e.file("A1").body) != "newest" {
		t.Fatalf("the newest content must update the file in place, got %d files, A1=%q", e.count(), e.file("A1").body)
	}
}

func TestStreamWriteAdoptedStaleCreateIsUpdatedWithoutIdentityFields(t *testing.T) {
	committed := make(chan struct{}, 1)
	e := newWEnvWrap(t, commitThenSleep(http.MethodPost, 600*time.Millisecond, committed))
	e.fake.strict = true
	sf, err := e.w.Create("docs/n.txt")
	if err != nil {
		t.Fatal(err)
	}
	sf.WriteAt([]byte("v1"), 0)
	e.w.Release(sf, true)
	<-committed
	again, err := e.w.OpenWrite(context.Background(), "docs/n.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	again.WriteAt([]byte("v2"), 0)
	e.w.Release(again, true)
	e.wait(func() bool { return e.w.Pending() == 0 })
	live := e.remoteByName("n.txt")
	if live == nil || string(live.body) != "v2" || e.count() != 1 {
		t.Fatalf("expected a single n.txt with the final content, got %d files, live=%+v", e.count(), live)
	}
}

func TestStreamWriteRemoveDuringCreateWithLostResponseLeavesNoOrphan(t *testing.T) {
	var mu sync.Mutex
	first := true
	started := make(chan struct{}, 1)
	e := newWEnvWrap(t, func(fake *wfake) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isCreate := r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload")
			mu.Lock()
			hit := isCreate && first
			if hit {
				first = false
			}
			mu.Unlock()
			if !hit {
				fake.serve(w, r)
				return
			}
			started <- struct{}{}
			time.Sleep(300 * time.Millisecond)
			fake.serve(w, r)
			hj, ok := w.(http.Hijacker)
			if !ok {
				return
			}
			c, _, err := hj.Hijack()
			if err == nil {
				c.Close()
			}
		})
	})
	old := streamRetryUpload
	streamRetryUpload = 50 * time.Millisecond
	t.Cleanup(func() { streamRetryUpload = old })
	sf, err := e.w.Create("docs/n.txt")
	if err != nil {
		t.Fatal(err)
	}
	sf.WriteAt([]byte("data"), 0)
	e.w.Release(sf, true)
	<-started
	if err := e.w.Remove(context.Background(), "docs/n.txt"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if f := e.remoteByName("n.txt"); f != nil {
		t.Fatalf("a file deleted during its upload was left on Drive as %s", f.ID)
	}
}
