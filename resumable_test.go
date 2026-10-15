package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

type fakeUpload struct {
	mu        sync.Mutex
	got       []byte
	failAfter int
	puts      int
	starts    int
}

func (f *fakeUpload) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost:
			f.starts++
			w.Header().Set("Location", "http://"+r.Host+"/session")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut:
			cr := r.Header.Get("Content-Range")
			var total int64
			if strings.HasPrefix(cr, "bytes */") {
				fmt.Sscanf(cr, "bytes */%d", &total)
			} else {
				var start, end int64
				fmt.Sscanf(cr, "bytes %d-%d/%d", &start, &end, &total)
				if start != int64(len(f.got)) {
					t.Errorf("chunk starts at %d, have %d", start, len(f.got))
				}
				f.puts++
				if f.failAfter > 0 && f.puts > f.failAfter {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				b, _ := io.ReadAll(r.Body)
				f.got = append(f.got, b...)
			}
			if int64(len(f.got)) == total {
				sum := md5.Sum(f.got)
				fmt.Fprintf(w, `{"id":"new","md5Checksum":"%s"}`, hex.EncodeToString(sum[:]))
				return
			}
			if len(f.got) > 0 {
				w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(f.got)-1))
			}
			w.WriteHeader(http.StatusPermanentRedirect)
		}
	})
}

func TestResumableUploadResumesAfterFailure(t *testing.T) {
	data := make([]byte, resumableChunk*2+12345)
	rand.Read(data)
	file := filepath.Join(t.TempDir(), "big.bin")
	os.WriteFile(file, data, 0o644)

	fake := &fakeUpload{failAfter: 1}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	store := loadSessions(filepath.Join(t.TempDir(), "s.json"))
	r := &resumer{client: srv.Client(), sessions: store, endpoint: srv.URL + "/upload"}
	target := uploadTarget{key: "k", name: "big.bin", parent: "p", mtime: time.Now()}

	if _, err := r.upload(context.Background(), target, file, int64(len(data)), nil, nil); err == nil {
		t.Fatal("expected first attempt to fail")
	}
	if _, ok := store.get("k"); !ok {
		t.Fatal("session not saved for resume")
	}
	reloaded := loadSessions(store.path)
	r.sessions = reloaded
	fake.failAfter = 0
	res, err := r.upload(context.Background(), target, file, int64(len(data)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fake.starts != 1 || !bytes.Equal(fake.got, data) || res.Id != "new" {
		t.Fatalf("starts=%d match=%v", fake.starts, bytes.Equal(fake.got, data))
	}
	if _, ok := reloaded.get("k"); ok {
		t.Fatal("session not removed after success")
	}
}

func TestResumableSessionGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, []byte("x"), 0o644)
	store := loadSessions(filepath.Join(t.TempDir(), "s.json"))
	store.put("k", uploadSession{URI: srv.URL + "/old", Created: time.Now()})
	r := &resumer{client: srv.Client(), sessions: store, endpoint: srv.URL}
	if _, err := r.upload(context.Background(), uploadTarget{key: "k"}, file, 1, nil, nil); err == nil {
		t.Fatal("expected error")
	}
	if _, ok := store.get("k"); ok {
		t.Fatal("expired session kept")
	}
}

func TestDownloadResumesPartialFile(t *testing.T) {
	data := make([]byte, 300000)
	rand.Read(data)
	sum := md5.Sum(data)
	want := hex.EncodeToString(sum[:])
	var ranges []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(data))
	}))
	defer api.Close()
	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(api.Client()), option.WithEndpoint(api.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(partName(dir, "id1", "", want), data[:100000], 0o600)
	target := filepath.Join(dir, "out.bin")
	if err := downloadFile(context.Background(), srv, "id1", "", want, 0, target, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, data) || len(ranges) != 1 || ranges[0] != "bytes=100000-" {
		t.Fatalf("match=%v ranges=%v", bytes.Equal(got, data), ranges)
	}
	if _, err := os.Stat(partName(dir, "id1", "", want)); !os.IsNotExist(err) {
		t.Fatal("part file left behind")
	}
}

func TestParseRangeEnd(t *testing.T) {
	if parseRangeEnd("bytes=0-99") != 100 || parseRangeEnd("") != 0 || parseRangeEnd("bytes=0-x") != 0 {
		t.Fatal("parseRangeEnd wrong")
	}
}

func TestStalePartCleanup(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, tmpPrefix+"part-old")
	fresh := filepath.Join(root, tmpPrefix+"part-new")
	os.WriteFile(old, []byte("x"), 0o600)
	os.WriteFile(fresh, []byte("x"), 0o600)
	past := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(old, past, past)
	if _, err := scanLocal(root, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("stale part not removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh part removed")
	}
}

func TestNewSessionSkipsStatusQuery(t *testing.T) {
	data := make([]byte, 1000)
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, data, 0o644)
	fake := &fakeUpload{}
	var queries int
	h := fake.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
			queries++
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	var progress []int64
	r := &resumer{client: srv.Client(), sessions: loadSessions(filepath.Join(t.TempDir(), "s.json")), endpoint: srv.URL}
	if _, err := r.upload(context.Background(), uploadTarget{key: "k"}, file, int64(len(data)), nil, func(o int64) { progress = append(progress, o) }); err != nil {
		t.Fatal(err)
	}
	if queries != 0 || len(progress) == 0 {
		t.Fatalf("queries=%d progress=%v", queries, progress)
	}
}

func TestCleanStaleParts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Photos [2024]")
	os.Mkdir(dir, 0o755)
	old := filepath.Join(dir, tmpPrefix+"part-x")
	keep := filepath.Join(dir, "photo.jpg")
	os.WriteFile(old, nil, 0o600)
	os.WriteFile(keep, nil, 0o600)
	past := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(old, past, past)
	os.Chtimes(keep, past, past)
	cleanStaleParts(dir)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("stale part kept")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("user file removed")
	}
}

func TestSyncerDownloadAll(t *testing.T) {
	files := map[string][]byte{"good1": []byte("one"), "good2": []byte("two")}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := filepath.Base(r.URL.Path)
		if id == "bad" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":403,"message":"abusive","errors":[{"reason":"cannotDownloadAbusiveFile"}]}}`))
			return
		}
		w.Write(files[id])
	}))
	defer api.Close()
	srv, _ := drive.NewService(context.Background(), option.WithHTTPClient(api.Client()), option.WithEndpoint(api.URL+"/"))
	sum := func(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }
	root := t.TempDir()
	st, _ := loadState(filepath.Join(t.TempDir(), "s.json"))
	var last Progress
	s := &Syncer{srv: srv, paths: Paths{Root: root}, st: st, progress: func(p Progress) { last = p }}
	errs := s.downloadAll(context.Background(), []pendingDownload{
		{"a.txt", RemoteFile{ID: "good1", MD5: sum(files["good1"]), Size: 3}},
		{"d/b.txt", RemoteFile{ID: "good2", MD5: sum(files["good2"]), Size: 3}},
		{"x.bin", RemoteFile{ID: "bad", MD5: "m", Size: 9}},
	})
	if len(errs) != 1 || st.Unavailable["x.bin"] != "m" {
		t.Fatalf("errs %v unavailable %v", errs, st.Unavailable)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "d", "b.txt")); string(b) != "two" || st.Files["a.txt"].MD5 == "" {
		t.Fatal("good files not downloaded or recorded")
	}
	if last.FilesDone != 3 || last.Downloaded != 2 || last.Uploaded != 0 || last.Phase != "downloading" {
		t.Fatalf("progress %+v", last)
	}
}
