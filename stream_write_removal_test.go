package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
)

func TestStreamWriteRemoveDuringUploadKeepsTheStageUntilTheSendEnds(t *testing.T) {
	e := newWEnv(t)
	e.fake.mu.Lock()
	e.fake.delay = 400 * time.Millisecond
	e.fake.mu.Unlock()
	sf, err := e.w.Create("docs/n.txt")
	if err != nil {
		t.Fatal(err)
	}
	stage := sf.stage
	sf.WriteAt([]byte("data"), 0)
	e.w.Release(sf, true)
	time.Sleep(200 * time.Millisecond)
	if err := e.w.Remove(context.Background(), "docs/n.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("the staging file must stay readable while the upload is in flight: %v", err)
	}
	e.wait(func() bool { _, err := os.Stat(stage); return os.IsNotExist(err) })
	e.wait(func() bool { return e.remoteByName("n.txt") == nil })
}

func TestStreamWriteRemoveDuringUploadDoesNotReAddTheTrashedFileToTheIndex(t *testing.T) {
	mtimeStarted := make(chan struct{}, 1)
	e := newWEnvWrap(t, func(fake *wfake) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPatch && !strings.HasPrefix(r.URL.Path, "/upload") {
				b, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(b))
				if bytes.Contains(b, []byte("modifiedTime")) {
					select {
					case mtimeStarted <- struct{}{}:
					default:
					}
					time.Sleep(500 * time.Millisecond)
				}
			}
			fake.serve(w, r)
		})
	})
	e.addRemote("docs/a.txt", "A1", "old")
	e.index.mu.Lock()
	e.index.Files["A1"] = &drive.File{Id: "A1", Name: "a.txt", Parents: []string{"D"}}
	e.index.mu.Unlock()
	e.fake.mu.Lock()
	e.fake.delay = 300 * time.Millisecond
	e.fake.mu.Unlock()
	sf, err := e.w.OpenWrite(context.Background(), "docs/a.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	sf.WriteAt([]byte("new"), 0)
	e.w.Release(sf, true)
	time.Sleep(150 * time.Millisecond)
	if err := e.w.SetMTime(context.Background(), "docs/a.txt", time.Date(2023, 1, 2, 3, 4, 5, 0, time.UTC).UnixNano()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-mtimeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the upload never reached the modified time step")
	}
	if err := e.w.Remove(context.Background(), "docs/a.txt"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second)
	e.index.mu.Lock()
	ghost := e.index.Files["A1"]
	e.index.mu.Unlock()
	if ghost != nil {
		t.Fatal("a file removed during its upload must not come back into the index")
	}
}
