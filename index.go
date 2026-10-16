package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

const fullListEvery = 24 * time.Hour

type driveIndex struct {
	mu     sync.Mutex
	path   string
	RootID string                 `json:"rootId"`
	Token  string                 `json:"token"`
	FullAt time.Time              `json:"fullAt"`
	WalSeq uint64                 `json:"walSeq"`
	Files  map[string]*drive.File `json:"files"`

	version  uint64
	dirty    bool
	savedAt  time.Time
	built    *Remote
	builtVer uint64
	idPath   map[string]string
	wal      *os.File
}

type indexOp struct {
	Seq uint64      `json:"seq"`
	Put *drive.File `json:"put,omitempty"`
	Del []string    `json:"del,omitempty"`
}

func (x *driveIndex) walPath() string { return x.path + ".log" }

func (x *driveIndex) logOp(op indexOp) {
	if x.path == "" {
		return
	}
	x.WalSeq++
	op.Seq = x.WalSeq
	if x.wal == nil {
		f, err := os.OpenFile(x.walPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Printf("drive index log: %v", err)
			return
		}
		x.wal = f
	}
	b, err := json.Marshal(op)
	if err == nil {
		_, err = x.wal.Write(append(b, '\n'))
	}
	if err != nil {
		log.Printf("drive index log: %v", err)
	}
}

func (x *driveIndex) replay() {
	snapSeq := x.WalSeq
	f, err := os.Open(x.walPath())
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var op indexOp
		if json.Unmarshal(sc.Bytes(), &op) != nil || op.Seq <= snapSeq {
			continue
		}
		if op.Seq > x.WalSeq {
			x.WalSeq = op.Seq
		}
		if op.Put != nil && op.Put.Id != "" {
			x.Files[op.Put.Id] = op.Put
		}
		for _, id := range op.Del {
			delete(x.Files, id)
		}
	}
	x.changed()
}

const indexSaveEvery = time.Minute

func sameSpot(a, b *drive.File) bool {
	return a.Name == b.Name && slices.Equal(a.Parents, b.Parents) && a.MimeType == b.MimeType &&
		a.MimeType != folderMime && !strings.HasPrefix(a.MimeType, gappsPrefix) && a.Md5Checksum != "" && b.Md5Checksum != ""
}

func (x *driveIndex) put(f *drive.File) {
	cur, ok := x.Files[f.Id]
	x.Files[f.Id] = f
	x.dirty = true
	if ok && x.built != nil && x.builtVer == x.version && sameSpot(cur, f) {
		if p, ok := x.idPath[f.Id]; ok {
			if rf, ok := x.built.Files[p]; ok && rf.ID == f.Id {
				if t, err := time.Parse(time.RFC3339, f.ModifiedTime); err == nil {
					x.built.Files[p] = RemoteFile{ID: f.Id, MD5: f.Md5Checksum, Size: f.Size, MTime: t.UnixNano()}
					return
				}
			}
		}
	}
	x.version++
}

func (x *driveIndex) changed() {
	x.version++
	x.dirty = true
}

func (p Paths) index() string { return filepath.Join(p.StateDir, "drive-index.json") }

func loadIndex(path string) *driveIndex {
	x := &driveIndex{path: path}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, x); err != nil {
			log.Printf("drive index unreadable, rebuilding: %v", err)
			x = &driveIndex{path: path}
		}
	}
	if x.Files == nil {
		x.Files = map[string]*drive.File{}
	}
	x.replay()
	return x
}

func (x *driveIndex) save() error {
	b, err := json.Marshal(x)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(x.path, b, 0o600); err != nil {
		return err
	}
	if x.wal != nil {
		x.wal.Close()
		x.wal = nil
	}
	if err := os.Remove(x.walPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func tokenExpired(err error) bool {
	var g *googleapi.Error
	return errors.As(err, &g) && (g.Code == http.StatusNotFound || g.Code == http.StatusGone || g.Code == http.StatusBadRequest)
}

func (x *driveIndex) note(f *drive.File, name, parent string) {
	if x == nil || f == nil || f.Id == "" {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	cur, known := x.Files[f.Id]
	if !known && parent == "" {
		return
	}
	next := &drive.File{Id: f.Id, Name: name, Parents: []string{parent}, MimeType: "application/octet-stream"}
	if known {
		copied := *cur
		next = &copied
	}
	next.Md5Checksum, next.ModifiedTime, next.Size = f.Md5Checksum, f.ModifiedTime, f.Size
	if x.Files == nil {
		x.Files = map[string]*drive.File{}
	}
	x.put(next)
	x.logOp(indexOp{Put: next})
}

func (x *driveIndex) noteFolder(id, name, parent string) {
	if x == nil || id == "" || parent == "" {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.Files[id]; ok {
		return
	}
	if x.Files == nil {
		x.Files = map[string]*drive.File{}
	}
	x.Files[id] = &drive.File{Id: id, Name: name, Parents: []string{parent}, MimeType: folderMime}
	x.changed()
	x.logOp(indexOp{Put: x.Files[id]})
}

func (x *driveIndex) moved(id, name, parent string) {
	if x == nil || id == "" {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	cur, ok := x.Files[id]
	if !ok {
		return
	}
	next := *cur
	next.Name, next.Parents = name, []string{parent}
	x.Files[id] = &next
	x.changed()
	x.logOp(indexOp{Put: &next})
}

func (x *driveIndex) ancestors(id string, into map[string]bool) {
	if x == nil {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for depth := 0; depth < 64; depth++ {
		f, ok := x.Files[id]
		if !ok || len(f.Parents) == 0 {
			return
		}
		id = f.Parents[0]
		into[id] = true
	}
}

func (x *driveIndex) modTime(id string) string {
	if x == nil {
		return ""
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if f, ok := x.Files[id]; ok {
		return f.ModifiedTime
	}
	return ""
}

func (x *driveIndex) forget(id string) {
	if x == nil || id == "" {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.Files, id)
	x.changed()
	x.logOp(indexOp{Del: []string{id}})
}

func (x *driveIndex) forgetTree(id string) {
	if x == nil || id == "" {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	gone := map[string]bool{id: true}
	for changed := true; changed; {
		changed = false
		for fid, f := range x.Files {
			if !gone[fid] && len(f.Parents) > 0 && gone[f.Parents[0]] {
				gone[fid], changed = true, true
			}
		}
	}
	ids := make([]string, 0, len(gone))
	for fid := range gone {
		delete(x.Files, fid)
		ids = append(ids, fid)
	}
	x.changed()
	for chunk := range slices.Chunk(ids, 1000) {
		x.logOp(indexOp{Del: chunk})
	}
}

func (x *driveIndex) snapshot() (*Remote, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	rem, err := x.current()
	if err != nil {
		return nil, err
	}
	return rem.clone(), nil
}

func (x *driveIndex) remote(ctx context.Context, srv *drive.Service) (*Remote, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	var err error
	if x.Token == "" || time.Since(x.FullAt) > fullListEvery {
		err = x.full(ctx, srv)
	} else if err = x.changes(ctx, srv); tokenExpired(err) {
		err = x.full(ctx, srv)
	}
	if err != nil {
		return nil, err
	}
	if now := time.Now(); x.dirty && now.Sub(x.savedAt) >= indexSaveEvery {
		if err := x.save(); err != nil {
			log.Printf("save drive index: %v", err)
		} else {
			x.dirty, x.savedAt = false, now
		}
	}
	rem, err := x.current()
	if err != nil {
		return nil, err
	}
	return rem.clone(), nil
}

func (x *driveIndex) current() (*Remote, error) {
	if x.built != nil && x.builtVer == x.version {
		return x.built, nil
	}
	rem, err := buildRemote(x.Files, x.RootID)
	if err != nil {
		return nil, err
	}
	idPath := make(map[string]string, len(rem.Files))
	for p, f := range rem.Files {
		idPath[f.ID] = p
	}
	x.built, x.builtVer, x.idPath = rem, x.version, idPath
	return rem, nil
}

func (x *driveIndex) full(ctx context.Context, srv *drive.Service) error {
	start, err := srv.Changes.GetStartPageToken().Context(ctx).Do()
	if err != nil {
		return err
	}
	rootID, err := rootFolderID(ctx, srv)
	if err != nil {
		return err
	}
	all, err := listAll(ctx, srv)
	if err != nil {
		return err
	}
	x.RootID = rootID
	x.Files, x.Token, x.FullAt = all, start.StartPageToken, time.Now()
	x.changed()
	return nil
}

func (x *driveIndex) changes(ctx context.Context, srv *drive.Service) error {
	var all []*drive.Change
	token := x.Token
	for {
		page, err := srv.Changes.List(token).Spaces("drive").IncludeRemoved(true).PageSize(1000).
			Fields("nextPageToken,newStartPageToken,changes(fileId,removed,file(" + remoteFields + ",trashed))").
			Context(ctx).Do()
		if err != nil {
			return err
		}
		all = append(all, page.Changes...)
		if page.NextPageToken != "" {
			token = page.NextPageToken
			continue
		}
		x.apply(all)
		if page.NewStartPageToken != x.Token {
			x.Token, x.dirty = page.NewStartPageToken, true
		}
		return nil
	}
}

func (x *driveIndex) apply(changes []*drive.Change) {
	for _, c := range changes {
		switch {
		case c.FileId == "":
		case c.Removed || c.File == nil || c.File.Trashed:
			if _, ok := x.Files[c.FileId]; ok {
				delete(x.Files, c.FileId)
				x.changed()
			}
		default:
			if cur, ok := x.Files[c.FileId]; ok && cur.Version != 0 && c.File.Version != 0 && cur.Version > c.File.Version {
				continue
			}
			c.File.Trashed = false
			x.put(c.File)
		}
	}
}

func (x *driveIndex) remoteOrList(ctx context.Context, srv *drive.Service) (*Remote, error) {
	if x == nil {
		return listRemote(ctx, srv)
	}
	return x.remote(ctx, srv)
}
