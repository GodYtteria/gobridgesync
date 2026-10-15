package main

import (
	"context"
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

func TestCategoryFor(t *testing.T) {
	oct := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	cases := []struct {
		name string
		size int64
		dir  bool
		want string
	}{
		{"IMG_1.HEIC", 1, false, "Pictures/2026-10"},
		{"clip.MP4", 1, false, "Videos/2026-10"},
		{"song.flac", 1, false, "Music"},
		{"report.pdf", 1, false, "Documents"},
		{"Notes.gdoc", 1, false, "Documents"},
		{"backup.tar.gz", 1, false, "Archives"},
		{"setup.AppImage", 1, false, "Installers"},
		{"llama.gguf", 1, false, "Models"},
		{"weights.bin", 200 << 20, false, "Models"},
		{"small.bin", 10, false, "Other/BIN"},
		{"main.go", 1, false, "Code"},
		{"part.stl", 1, false, "3D"},
		{"font.woff2", 1, false, "Fonts"},
		{"id_rsa.key", 1, false, "Other/KEY"},
		{"README", 1, false, "Other"},
		{"project", 0, true, "Folders"},
	}
	for _, c := range cases {
		if got := categoryFor(c.name, c.size, oct, c.dir); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestIncomplete(t *testing.T) {
	for _, n := range []string{"a.part", "b.crdownload", "~$doc.docx", ".~lock.x#", tmpPrefix + "x"} {
		if !incomplete(n) {
			t.Errorf("%s should be skipped", n)
		}
	}
	if incomplete("photo.jpg") {
		t.Error("photo.jpg is complete")
	}
}

type fakeDrive struct {
	mu        sync.Mutex
	updates   []string
	creates   int
	failMoves bool
}

func (f *fakeDrive) server(t *testing.T) *drive.Service {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/generateIds"):
			ids := []string{}
			for i := range idBatch {
				ids = append(ids, fmt.Sprintf(`"id%d"`, i))
			}
			fmt.Fprintf(w, `{"ids":[%s]}`, strings.Join(ids, ","))
		case r.Method == http.MethodPost:
			f.creates++
			io.Copy(io.Discard, r.Body)
			w.Write([]byte(`{"id":"x"}`))
		case r.Method == http.MethodPatch && f.failMoves:
			http.Error(w, `{"error":{"code":403,"message":"forbidden"}}`, http.StatusForbidden)
		case r.Method == http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			f.updates = append(f.updates, filepath.Base(r.URL.Path)+" "+r.URL.Query().Get("addParents")+" "+r.URL.Query().Get("removeParents")+" "+string(body))
			w.Write([]byte(`{"id":"x"}`))
		}
	}))
	t.Cleanup(srv.Close)
	d, _ := drive.NewService(context.Background(), option.WithHTTPClient(srv.Client()), option.WithEndpoint(srv.URL+"/"))
	return d
}

func newInboxSyncer(t *testing.T, srv *drive.Service) *Syncer {
	st, _ := loadState(filepath.Join(t.TempDir(), "s.json"))
	return &Syncer{srv: srv, paths: Paths{Root: t.TempDir()}, st: st, inbox: true}
}

func put(t *testing.T, s *Syncer, rel string, age time.Duration) {
	full := s.abs(rel)
	os.MkdirAll(filepath.Dir(full), 0o755)
	os.WriteFile(full, []byte("x"), 0o644)
	old := time.Now().Add(-age)
	os.Chtimes(full, old, old)
}

func TestSortInboxLocalAndRemote(t *testing.T) {
	fd := &fakeDrive{}
	s := newInboxSyncer(t, fd.server(t))
	remote := &Remote{
		Files:     map[string]RemoteFile{"Inbox/web.pdf": {ID: "w1", MD5: "m", MTime: time.Now().UnixNano()}, "Inbox/synced.zip": {ID: "z1", MD5: "z"}},
		Docs:      map[string]RemoteDoc{},
		Folders:   map[string]string{"": "root", "Inbox": "inbox", "Pictures": "pics"},
		Ambiguous: map[string]bool{},
	}
	s.st.Files["Inbox/synced.zip"] = Entry{ID: "z1", MD5: "z"}
	put(t, s, "Inbox/photo.jpg", time.Hour)
	put(t, s, "Inbox/synced.zip", time.Hour)
	put(t, s, "Inbox/fresh.png", 0)
	put(t, s, "Inbox/dl.crdownload", time.Hour)
	put(t, s, "Inbox/proj/main.go", time.Hour)
	put(t, s, "Pictures/2020-01/old.jpg", time.Hour)
	put(t, s, "Documents/web.pdf", time.Hour)

	if errs := s.sortInbox(context.Background(), remote); len(errs) != 0 {
		t.Fatal(errs)
	}
	month := time.Now().Add(-time.Hour).Local().Format("2006-01")
	for _, rel := range []string{"Pictures/" + month + "/photo.jpg", "Archives/synced.zip", "Folders/proj/main.go", "Inbox/fresh.png", "Inbox/dl.crdownload", "Pictures/2020-01/old.jpg"} {
		if _, err := os.Stat(s.abs(rel)); err != nil {
			t.Errorf("missing %s", rel)
		}
	}
	if _, ok := remote.Files["Documents/web (2).pdf"]; !ok {
		t.Errorf("web drop should be moved on Drive with a free name: %v", remote.Files)
	}
	if _, ok := s.st.Files["Archives/synced.zip"]; !ok || s.st.Files["Inbox/synced.zip"].ID != "" {
		t.Error("state not re-keyed")
	}
	if fd.creates < 1 || len(fd.updates) != 2 {
		t.Fatalf("expected 2 server-side moves, got %v", fd.updates)
	}
	for _, u := range fd.updates {
		if !strings.Contains(u, " inbox ") {
			t.Errorf("move must remove the Inbox parent: %s", u)
		}
	}
}

func TestSortInboxDisabledLeavesFiles(t *testing.T) {
	s := newInboxSyncer(t, nil)
	s.inbox = false
	put(t, s, "Inbox/photo.jpg", time.Hour)
	if s.inbox {
		t.Fatal("inbox should be off")
	}
	if (AppConfig{TwoWay: false}).inboxSort() || !(AppConfig{TwoWay: true}).inboxSort() {
		t.Fatal("inbox default should follow two-way sync")
	}
}

func TestSortInboxFollowsOtherComputer(t *testing.T) {
	fd := &fakeDrive{}
	s := newInboxSyncer(t, fd.server(t))
	remote := &Remote{
		Files:     map[string]RemoteFile{"Pictures/2026-10/photo.jpg": {ID: "p1", MD5: "m"}},
		Docs:      map[string]RemoteDoc{},
		Folders:   map[string]string{"": "root", "Inbox": "inbox", "Pictures": "pics", "Pictures/2026-10": "oct"},
		Ambiguous: map[string]bool{},
	}
	s.st.Files["Inbox/photo.jpg"] = Entry{ID: "p1", MD5: "m"}
	put(t, s, "Inbox/photo.jpg", time.Hour)
	if errs := s.sortInbox(context.Background(), remote); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, err := os.Stat(s.abs("Pictures/2026-10/photo.jpg")); err != nil {
		t.Fatal("local copy should follow the move made on the other computer")
	}
	if len(fd.updates) != 0 {
		t.Fatalf("must not move again on Drive: %v", fd.updates)
	}
	if _, ok := s.st.Files["Pictures/2026-10/photo.jpg"]; !ok {
		t.Fatal("state not re-keyed")
	}
}

func TestMoveItemPutsLocalFileBackWhenDriveFails(t *testing.T) {
	fd := &fakeDrive{failMoves: true}
	s := newInboxSyncer(t, fd.server(t))
	remote := &Remote{Files: map[string]RemoteFile{"Inbox/a.pdf": {ID: "a1"}}, Docs: map[string]RemoteDoc{}, Folders: map[string]string{"": "root", "Inbox": "inbox", "Documents": "docs"}, Ambiguous: map[string]bool{}}
	s.st.Files["Inbox/a.pdf"] = Entry{ID: "a1"}
	put(t, s, "Inbox/a.pdf", time.Hour)
	it := inboxItem{name: "a.pdf", id: "a1", local: true}
	if err := s.moveItem(context.Background(), remote, it, "inbox", "Inbox/a.pdf", "Documents/a.pdf"); err == nil {
		t.Fatal("expected Drive error")
	}
	if _, err := os.Stat(s.abs("Inbox/a.pdf")); err != nil {
		t.Fatal("local file not put back after the Drive move failed")
	}
	if _, ok := s.st.Files["Inbox/a.pdf"]; !ok {
		t.Fatal("state re-keyed although the move failed")
	}
}

func TestMoveItemUpdatesDriveIndex(t *testing.T) {
	fd := &fakeDrive{}
	s := newInboxSyncer(t, fd.server(t))
	s.index = &driveIndex{Files: map[string]*drive.File{"a1": {Id: "a1", Name: "a.pdf", Parents: []string{"inbox"}, MimeType: "application/pdf"}}}
	remote := &Remote{Files: map[string]RemoteFile{"Inbox/a.pdf": {ID: "a1"}}, Docs: map[string]RemoteDoc{}, Folders: map[string]string{"": "root", "Inbox": "inbox", "Documents": "docs"}, Ambiguous: map[string]bool{}}
	put(t, s, "Inbox/a.pdf", time.Hour)
	it := inboxItem{name: "a.pdf", id: "a1", local: true}
	if err := s.moveItem(context.Background(), remote, it, "inbox", "Inbox/a.pdf", "Documents/a.pdf"); err != nil {
		t.Fatal(err)
	}
	if f := s.index.Files["a1"]; f.Parents[0] != "docs" || f.Name != "a.pdf" {
		t.Fatalf("index not updated: %+v", f)
	}
}
