package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

var (
	errStreamExists   = errors.New("already exists")
	errStreamMissing  = errors.New("does not exist")
	errStreamNotEmpty = errors.New("folder is not empty")

	errStreamTooManyDeletes = errors.New("too many files deleted in a short time")
	errStreamIsDir          = errors.New("is a folder")
	errStreamNoSpace        = errors.New("not enough disk space")
	errStreamStale          = errors.New("changed on Drive; reopen it")
	errStreamBadName        = errors.New("name is not allowed")
)

var (
	streamRetryUpload    = 30 * time.Second
	streamRetryUploadMax = 10 * time.Minute
	streamMetaTimeout    = 2 * time.Minute
	streamUploadDelay    = 2 * time.Second
)

type stageFile struct {
	mu       sync.Mutex
	f        *os.File
	stage    string
	path     string
	baseID   string
	baseMD5  string
	isNew    bool
	opens    int
	writers  int
	replace  RemoteFile
	version  int64
	mtime    int64
	queued   bool
	running  bool
	timeSet  bool
	failures int
	cancel   context.CancelFunc
	again    bool
	done     bool
	metaMu   sync.Mutex
	conflict string
	sentMD5  string
}

func (s *stageFile) snapshot() (path, baseID, baseMD5 string, isNew bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path, s.baseID, s.baseMD5, s.isNew
}

func (s *stageFile) addFailure() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	return s.failures
}

func (s *stageFile) resetFailures() {
	s.mu.Lock()
	s.failures = 0
	s.mu.Unlock()
}

func uploadRetryDelay(failures int) time.Duration {
	return min(streamRetryUpload<<min(max(failures-1, 0), 5), streamRetryUploadMax)
}

func (s *stageFile) hasWriters() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writers > 0
}

func (s *stageFile) modTime() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mtime == 0 {
		return time.Now().UnixNano()
	}
	return s.mtime
}

func (s *stageFile) rawMTime() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mtime
}

func (s *stageFile) setMTime(mt int64) {
	s.mu.Lock()
	s.mtime = mt
	s.timeSet = true
	s.mu.Unlock()
}

func (s *stageFile) size() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := s.f.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}

func (s *stageFile) ReadAt(p []byte, off int64) (int, error) { return s.f.ReadAt(p, off) }

func (s *stageFile) WriteAt(p []byte, off int64) (int, error) {
	s.mu.Lock()
	s.version++
	s.mtime = time.Now().UnixNano()
	s.mu.Unlock()
	return s.f.WriteAt(p, off)
}

func (s *stageFile) Truncate(size int64) error {
	s.mu.Lock()
	s.version++
	s.mtime = time.Now().UnixNano()
	s.mu.Unlock()
	return s.f.Truncate(size)
}

type stageMeta struct {
	Path    string `json:"path"`
	BaseID  string `json:"baseId"`
	BaseMD5 string `json:"baseMd5"`
	IsNew   bool   `json:"isNew"`
	Open    bool   `json:"open"`

	SentMD5    string `json:"sentMd5,omitempty"`
	Conflict   string `json:"conflict,omitempty"`
	ReplaceID  string `json:"replaceId,omitempty"`
	ReplaceMD5 string `json:"replaceMd5,omitempty"`
}

type streamWriter struct {
	mu      sync.Mutex
	srv     *drive.Service
	dir     string
	cache   *streamCache
	snap    func() *Remote
	index   *driveIndex
	ids     *Remote
	changed func()
	files   map[string]*stageFile
	queue   chan *stageFile
	seq     int
	deletes deleteGuard
}

func newStreamWriter(srv *drive.Service, dir string, cache *streamCache, snap func() *Remote, index *driveIndex, journal *idJournal, changed func()) (*streamWriter, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if changed == nil {
		changed = func() {}
	}
	w := &streamWriter{srv: srv, dir: dir, cache: cache, snap: snap, index: index, ids: &Remote{journal: journal}, changed: changed,
		files: map[string]*stageFile{}, queue: make(chan *stageFile, 1024)}
	return w, w.reload()
}

func (w *streamWriter) reload() error {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(w.dir, e.Name()))
		var m stageMeta
		if err != nil || json.Unmarshal(b, &m) != nil || m.Path == "" {
			continue
		}
		stage := strings.TrimSuffix(filepath.Join(w.dir, e.Name()), ".json")
		f, err := os.OpenFile(stage, os.O_RDWR, 0o600)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("streaming: removing the leftover details for %s (its staging file is gone)", m.Path)
				os.Remove(filepath.Join(w.dir, e.Name()))
			}
			continue
		}
		known[filepath.Base(stage)] = true
		sf := &stageFile{f: f, stage: stage, path: m.Path, baseID: m.BaseID, baseMD5: m.BaseMD5, isNew: m.IsNew, version: 1,
			sentMD5: m.SentMD5, conflict: m.Conflict, replace: RemoteFile{ID: m.ReplaceID, MD5: m.ReplaceMD5}}
		if m.Open && m.IsNew {
			if info, serr := f.Stat(); serr == nil && info.Size() == 0 {
				f.Close()
				os.Remove(stage)
				os.Remove(stage + ".json")
				continue
			}
		}
		if m.Open && !m.IsNew && m.BaseMD5 != "" {
			if sum, _, serr := fileMD5Of(f); serr == nil && sum == m.BaseMD5 {
				f.Close()
				os.Remove(stage)
				os.Remove(stage + ".json")
				continue
			}
		}
		if m.Open {
			dir, name := splitPath(m.Path)
			sf.path = path.Join(dir, recoveredName(name, time.Now()))
			sf.baseID, sf.baseMD5, sf.isNew = "", "", true
			sf.sentMD5, sf.conflict = "", ""
			sf.replace = RemoteFile{}
			log.Printf("%s was still open when the app stopped; saving what was written as %s", m.Path, sf.path)
		}
		if other, dup := w.files[sf.path]; dup {
			sf.path = path.Join(path.Dir(sf.path), recoveredName(path.Base(other.path), time.Now().Add(time.Second)))
		}
		w.files[sf.path] = sf
		if err := w.saveMeta(sf); err != nil {
			log.Printf("saving details for %s: %v", sf.path, err)
		}
		w.enqueue(sf)
	}
	hasMeta := map[string]bool{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			hasMeta[strings.TrimSuffix(e.Name(), ".json")] = true
		}
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") && !known[e.Name()] && !hasMeta[e.Name()] {
			os.Remove(filepath.Join(w.dir, e.Name()))
		}
	}
	return nil
}

func recoveredName(name string, now time.Time) string {
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext) + " (recovered " + now.Format("20060102-150405") + ")" + ext
}

func (w *streamWriter) saveMeta(sf *stageFile) error {
	sf.metaMu.Lock()
	defer sf.metaMu.Unlock()
	sf.mu.Lock()
	m := stageMeta{Path: sf.path, BaseID: sf.baseID, BaseMD5: sf.baseMD5, IsNew: sf.isNew, Open: sf.writers > 0,
		SentMD5: sf.sentMD5, Conflict: sf.conflict, ReplaceID: sf.replace.ID, ReplaceMD5: sf.replace.MD5}
	done := sf.done
	sf.mu.Unlock()
	if done {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(sf.stage+".json", b, 0o600)
}

func (w *streamWriter) Start(ctx context.Context, workers int) {
	for i := 0; i < workers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case sf := <-w.queue:
					err := w.upload(ctx, sf)
					if err == nil {
						sf.resetFailures()
						continue
					}
					if ctx.Err() == nil && !sf.hasWriters() {
						p, _, _, _ := sf.snapshot()
						n := sf.addFailure()
						delay := uploadRetryDelay(n)
						log.Printf("saving %s to Google Drive: %v (attempt %d, will retry in %s)", p, err, n, delay)
						time.AfterFunc(delay, func() { w.enqueue(sf) })
					}
				}
			}
		}()
	}
}

func (w *streamWriter) enqueue(sf *stageFile) {
	sf.mu.Lock()
	switch {
	case sf.done:
		sf.mu.Unlock()
		return
	case sf.running:
		sf.again = true
		sf.mu.Unlock()
		return
	case sf.queued:
		sf.mu.Unlock()
		return
	}
	sf.queued = true
	sf.mu.Unlock()
	select {
	case w.queue <- sf:
	default:
		go func() { w.queue <- sf }()
	}
}

func (w *streamWriter) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.files)
}

func splitPath(p string) (dir, name string) {
	dir = path.Dir(p)
	if dir == "." {
		dir = ""
	}
	return dir, path.Base(p)
}

func (w *streamWriter) lookupPending(p string) *stageFile {
	w.mu.Lock()
	sf := w.files[p]
	w.mu.Unlock()
	if sf == nil {
		return nil
	}
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.done {
		return nil
	}
	return sf
}

func (w *streamWriter) PendingIn(dir string) []treeEntry {
	type item struct {
		name string
		sf   *stageFile
	}
	var items []item
	w.mu.Lock()
	for p, sf := range w.files {
		if d, name := splitPath(p); d == dir {
			items = append(items, item{name, sf})
		}
	}
	w.mu.Unlock()
	out := make([]treeEntry, 0, len(items))
	for _, it := range items {
		_, id, _, _ := it.sf.snapshot()
		out = append(out, treeEntry{Name: it.name, File: RemoteFile{ID: id, Size: it.sf.size(), MTime: it.sf.modTime()}})
	}
	return out
}

func (w *streamWriter) acquire(p string, write bool) *stageFile {
	w.mu.Lock()
	sf := w.files[p]
	if sf == nil {
		w.mu.Unlock()
		return nil
	}
	sf.mu.Lock()
	if sf.done {
		sf.mu.Unlock()
		w.mu.Unlock()
		return nil
	}
	sf.opens++
	first := false
	if write {
		sf.writers++
		first = sf.writers == 1
		sf.failures = 0
		if sf.cancel != nil {
			sf.cancel()
			sf.again = true
		}
	}
	sf.mu.Unlock()
	w.mu.Unlock()
	if first {
		if err := w.saveMeta(sf); err != nil {
			log.Printf("saving details for %s: %v", p, err)
		}
	}
	return sf
}

func (w *streamWriter) newStage() (*os.File, string, error) {
	w.mu.Lock()
	w.seq++
	name := filepath.Join(w.dir, fmt.Sprintf("%d-%d", time.Now().UnixNano(), w.seq))
	w.mu.Unlock()
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	return f, name, err
}

func (w *streamWriter) dirExists(r *Remote, dir string) bool {
	if dir == "" {
		return true
	}
	_, ok := r.Folders[dir]
	return ok
}

func (w *streamWriter) hidden(r *Remote, p string) bool {
	if _, doc := r.Docs[p]; doc {
		log.Printf("streaming: refusing to use the name %s: a Google Docs file already has this name", p)
		return true
	}
	if r.blocked(p) {
		log.Printf("streaming: refusing to use the name %s: Google Drive has more than one item with this name; rename them on drive.google.com first", p)
		return true
	}
	return false
}

func (w *streamWriter) Create(p string) (*stageFile, error) {
	dir, name := splitPath(p)
	if !validName(name) {
		return nil, errStreamBadName
	}
	r := w.snap()
	if r == nil || !w.dirExists(r, dir) {
		return nil, errStreamMissing
	}
	_, isFile := r.Files[p]
	_, isDir := r.Folders[p]
	if isFile || isDir || w.hidden(r, p) {
		return nil, errStreamExists
	}
	f, stage, err := w.newStage()
	if err != nil {
		return nil, err
	}
	sf := &stageFile{f: f, stage: stage, path: p, isNew: true, opens: 1, writers: 1}
	w.mu.Lock()
	if other := w.files[p]; other != nil {
		other.mu.Lock()
		live := !other.done
		other.mu.Unlock()
		if live {
			w.mu.Unlock()
			f.Close()
			os.Remove(stage)
			return nil, errStreamExists
		}
	}
	w.files[p] = sf
	w.mu.Unlock()
	if err := w.saveMeta(sf); err != nil {
		log.Printf("saving details for %s: %v", p, err)
	}
	return sf, nil
}

func (w *streamWriter) OpenWrite(ctx context.Context, p string, trunc bool) (*stageFile, error) {
	for attempt := 0; attempt < 5; attempt++ {
		if sf := w.acquire(p, true); sf != nil {
			if trunc {
				return sf, sf.Truncate(0)
			}
			return sf, nil
		}
		r := w.snap()
		if r == nil {
			return nil, errStreamMissing
		}
		if _, isDir := r.Folders[p]; isDir {
			return nil, errStreamIsDir
		}
		cur, ok := r.Files[p]
		if !ok {
			return nil, errStreamMissing
		}
		if !trunc && cur.Size > diskBudget(w.dir) {
			return nil, errStreamNoSpace
		}
		f, stage, err := w.newStage()
		if err != nil {
			return nil, err
		}
		if !trunc {
			if err := w.hydrate(ctx, f, cur); err != nil {
				f.Close()
				os.Remove(stage)
				return nil, err
			}
		}
		sf := &stageFile{f: f, stage: stage, path: p, baseID: cur.ID, baseMD5: cur.MD5, opens: 1, writers: 1, mtime: cur.MTime}
		w.mu.Lock()
		if other := w.files[p]; other != nil {
			other.mu.Lock()
			live := !other.done
			other.mu.Unlock()
			if live {
				w.mu.Unlock()
				f.Close()
				os.Remove(stage)
				continue
			}
		}
		w.files[p] = sf
		w.mu.Unlock()
		if trunc {
			sf.mu.Lock()
			sf.version++
			sf.mtime = time.Now().UnixNano()
			sf.mu.Unlock()
		}
		if err := w.saveMeta(sf); err != nil {
			log.Printf("saving details for %s: %v", p, err)
		}
		return sf, nil
	}
	return nil, errStreamStale
}

func (w *streamWriter) hydrate(ctx context.Context, f *os.File, cur RemoteFile) error {
	buf := make([]byte, streamChunk)
	fetch := driveRangeFetcher(w.srv, cur.ID)
	for off := int64(0); off < cur.Size; {
		n, err := w.cache.ReadAt(ctx, cur.ID, cur.MD5, cur.Size, buf, off, fetch)
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], off); werr != nil {
				return werr
			}
			off += int64(n)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n == 0 {
			break
		}
	}
	return nil
}

func (w *streamWriter) Release(sf *stageFile, write bool) {
	sf.mu.Lock()
	sf.opens--
	if write {
		sf.writers--
	}
	idle := sf.opens <= 0
	lastWriter := write && sf.writers <= 0
	changed := sf.version > 0 || sf.isNew
	done := sf.done
	running := sf.running
	untouched := sf.writers <= 0 && !changed
	sf.mu.Unlock()
	if done {
		if idle && !running {
			w.finish(sf)
		}
		return
	}
	if !write {
		if idle && untouched {
			w.discardIfIdle(sf)
		}
		return
	}
	if !lastWriter {
		return
	}
	if !changed {
		if err := w.saveMeta(sf); err != nil {
			log.Printf("saving details for %s: %v", sf.path, err)
		}
		w.discardIfIdle(sf)
		sf.mu.Lock()
		p, mt, timeSet := sf.path, sf.mtime, sf.timeSet
		sf.mu.Unlock()
		if timeSet {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), streamMetaTimeout)
				defer cancel()
				if err := w.SetMTime(ctx, p, mt); err != nil {
					log.Printf("streaming: setting the modified time of %s: %v", p, err)
				}
			}()
		}
		return
	}
	if err := w.saveMeta(sf); err != nil {
		log.Printf("saving details for %s: %v", sf.path, err)
	}
	if streamUploadDelay <= 0 {
		w.enqueue(sf)
		return
	}
	time.AfterFunc(streamUploadDelay, func() { w.enqueue(sf) })
}

func (w *streamWriter) discardIfIdle(sf *stageFile) {
	w.mu.Lock()
	sf.mu.Lock()
	if sf.opens > 0 || sf.writers > 0 || sf.done || sf.queued || sf.running || sf.version > 0 {
		sf.mu.Unlock()
		w.mu.Unlock()
		return
	}
	sf.done = true
	p := sf.path
	if w.files[p] == sf {
		delete(w.files, p)
	}
	sf.mu.Unlock()
	w.mu.Unlock()
	w.finish(sf)
}

func (w *streamWriter) discard(sf *stageFile) {
	w.mu.Lock()
	sf.mu.Lock()
	sf.done = true
	p := sf.path
	idle := sf.opens <= 0 && !sf.running
	if w.files[p] == sf {
		delete(w.files, p)
	}
	sf.mu.Unlock()
	w.mu.Unlock()
	if idle {
		w.finish(sf)
	}
}

func (w *streamWriter) finish(sf *stageFile) {
	sf.metaMu.Lock()
	defer sf.metaMu.Unlock()
	sf.f.Close()
	os.Remove(sf.stage)
	os.Remove(sf.stage + ".json")
}

func fileMD5Of(f *os.File) (string, int64, error) {
	h := md5.New()
	n, err := io.Copy(h, io.NewSectionReader(f, 0, 1<<62))
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func (w *streamWriter) parentID(r *Remote, dir string) (string, error) {
	if dir == "" {
		return "root", nil
	}
	id, ok := r.Folders[dir]
	if !ok {
		return "", errStreamMissing
	}
	return id, nil
}

func (w *streamWriter) upload(ctx context.Context, sf *stageFile) error {
	sf.mu.Lock()
	sf.queued = false
	if sf.done || sf.writers > 0 || sf.running {
		sf.mu.Unlock()
		return nil
	}
	sf.running = true
	ctx, cancel := context.WithCancel(ctx)
	sf.cancel = cancel
	defer func() {
		cancel()
		sf.mu.Lock()
		sf.running = false
		sf.cancel = nil
		again := sf.again && !sf.done
		sf.again = false
		release := sf.done && sf.opens <= 0
		sf.mu.Unlock()
		if release {
			w.finish(sf)
		}
		if again {
			w.enqueue(sf)
		}
	}()
	version := sf.version
	p, baseID, baseMD5, isNew, sentMD5 := sf.path, sf.baseID, sf.baseMD5, sf.isNew, sf.sentMD5
	sf.mu.Unlock()

	sum, _, err := fileMD5Of(sf.f)
	if err != nil {
		return err
	}
	r := w.snap()
	if r == nil {
		return errStreamMissing
	}
	dir, name := splitPath(p)
	parent, err := w.parentID(r, dir)
	if err != nil {
		return err
	}
	if !isNew {
		var cur *drive.File
		gerr := retryRead(ctx, func() (err error) {
			cur, err = w.srv.Files.Get(baseID).Fields("id,md5Checksum,trashed,modifiedTime").Context(ctx).Do()
			return err
		})
		var apiErr *googleapi.Error
		switch {
		case errors.As(gerr, &apiErr) && apiErr.Code == http.StatusNotFound:
			isNew = true
		case gerr != nil:
			return gerr
		case committedOnDrive(cur, sum, sentMD5):
			baseMD5 = cur.Md5Checksum
			sf.mu.Lock()
			sf.baseMD5 = baseMD5
			sf.mu.Unlock()
			log.Printf("streaming: %s already has this content on Drive (an earlier save finished); not a conflict", p)
		case cur.Trashed || cur.Md5Checksum != baseMD5:
			isNew = true
			var first bool
			name, first = sf.conflictFor(name)
			if first {
				if err := w.saveMeta(sf); err != nil {
					log.Printf("saving details for %s: %v", p, err)
				}
			}
			log.Printf("saving %s: changed on Drive in the meantime; keeping both (%s)", p, name)
		}
		if !isNew && sum == baseMD5 {
			same, err := w.settleUnchanged(ctx, sf, p, baseID, name, parent, cur, version)
			if err != nil {
				return err
			}
			if same {
				return nil
			}
		}
	}
	if !isNew {
		sf.mu.Lock()
		sf.sentMD5 = sum
		sf.mu.Unlock()
		if err := w.saveMeta(sf); err != nil {
			log.Printf("saving details for %s: %v", p, err)
		}
	}
	sentMT := sf.rawMTime()
	meta := &drive.File{ModifiedTime: driveTime(sentMT)}
	var res *drive.File
	idKey := ""
	if isNew {
		idKey = "stream:" + filepath.Base(sf.stage)
		id, err := w.ids.newID(ctx, w.srv, idKey)
		if err != nil {
			return err
		}
		meta.Id, meta.Name, meta.Parents = id, name, []string{parent}
		res, err = w.send(ctx, sf, func(f io.Reader) (*drive.File, error) {
			return w.srv.Files.Create(meta).Media(f, googleapi.ChunkSize(uploadChunk)).Fields(fileFields).Context(ctx).Do()
		})
		if alreadyCreated(err) {
			res, err = adoptCreated(ctx, w.srv, id, sum)
			if errors.Is(err, errAdoptTrashed) {
				w.ids.releaseID(idKey)
			}
			if errors.Is(err, errAdoptStale) && res != nil {
				created := res.Id
				res, err = w.send(ctx, sf, func(f io.Reader) (*drive.File, error) {
					return w.srv.Files.Update(created, &drive.File{ModifiedTime: meta.ModifiedTime}).Media(f, googleapi.ChunkSize(uploadChunk)).Fields(fileFields).Context(ctx).Do()
				})
			}
		}
		if err != nil {
			if w.dropOrphan(ctx, sf, p, id, idKey) {
				return nil
			}
			return err
		}
	} else {
		res, err = w.send(ctx, sf, func(f io.Reader) (*drive.File, error) {
			return w.srv.Files.Update(baseID, meta).Media(f, googleapi.ChunkSize(uploadChunk)).Fields(fileFields).Context(ctx).Do()
		})
		if err != nil {
			return err
		}
	}
	sf.mu.Lock()
	removed := sf.done
	sf.mu.Unlock()
	if removed {
		if idKey != "" {
			w.ids.releaseID(idKey)
		}
		if isNew {
			if err := w.trashRemote(ctx, res.Id, ""); err != nil {
				log.Printf("removing %s after it was deleted during saving: %v", p, err)
			}
		}
		return nil
	}
	if res.Md5Checksum != "" && res.Md5Checksum != sum {
		sf.mu.Lock()
		changedMeanwhile := sf.version != version
		if changedMeanwhile {
			sf.isNew, sf.baseID, sf.baseMD5 = false, res.Id, res.Md5Checksum
			sf.again = true
		}
		sf.mu.Unlock()
		if changedMeanwhile {
			return nil
		}
		return fmt.Errorf("Google Drive stored different content (md5 %s, expected %s)", res.Md5Checksum, sum)
	}
	for attempt := 0; ; attempt++ {
		sf.mu.Lock()
		nowPath := sf.path
		sf.mu.Unlock()
		if nowPath != p && name == path.Base(p) {
			newDir, newName := splitPath(nowPath)
			if r2 := w.snap(); r2 != nil {
				if newParent, perr := w.parentID(r2, newDir); perr == nil {
					merr := retry(ctx, func() error {
						call := w.srv.Files.Update(res.Id, &drive.File{Name: newName}).Fields("id").Context(ctx)
						if newParent != parent {
							call = call.AddParents(newParent).RemoveParents(parent)
						}
						_, err := call.Do()
						return err
					})
					if merr != nil {
						return merr
					}
					w.index.moved(res.Id, newName, newParent)
					name, parent, dir, p = newName, newParent, newDir, nowPath
				}
			}
		}
		if mt := sf.rawMTime(); mt != 0 && mt != sentMT {
			if fixed, err := w.setRemoteMTime(ctx, res.Id, mt); err != nil {
				log.Printf("streaming: setting the modified time of %s: %v", p, err)
			} else {
				res = fixed
			}
		}
		if !sf.isDone() {
			w.index.note(res, name, parent)
			w.changed()
			log.Printf("streaming: saved %s to Google Drive (id %s, %s)", p, res.Id, humanBytes(res.Size))
		}
		sf.mu.Lock()
		rep := sf.replace
		sf.replace = RemoteFile{}
		sf.mu.Unlock()
		if rep.ID != "" && rep.ID != res.Id {
			if err := w.trashGuarded(ctx, rep.ID, rep.MD5); err != nil {
				log.Printf("replacing the old %s: %v", name, err)
			} else {
				w.index.forget(rep.ID)
				w.changed()
			}
		}

		sf.mu.Lock()
		if sf.done {
			sf.mu.Unlock()
			if idKey != "" {
				w.ids.releaseID(idKey)
			}
			if isNew {
				if err := w.trashRemote(ctx, res.Id, ""); err != nil {
					log.Printf("removing %s after it was deleted during saving: %v", p, err)
					return nil
				}
			}
			w.index.forget(res.Id)
			w.changed()
			return nil
		}
		if sf.path != p && attempt < 5 {
			sf.mu.Unlock()
			continue
		}
		rewritten := sf.version != version || sf.writers > 0
		if rewritten {
			sf.isNew, sf.baseID, sf.baseMD5 = false, res.Id, sum
		} else {
			sf.done = true
		}
		sf.mu.Unlock()
		if rewritten && idKey != "" {
			w.ids.releaseID(idKey)
		}
		if !rewritten {
			if idKey != "" {
				w.ids.releaseID(idKey)
			}
			w.discard(sf)
			return nil
		}
		break
	}
	if err := w.saveMeta(sf); err != nil {
		log.Printf("saving details for %s: %v", p, err)
	}
	sf.mu.Lock()
	again := sf.writers == 0
	sf.mu.Unlock()
	if again {
		w.enqueue(sf)
	}
	return nil
}

func (w *streamWriter) send(ctx context.Context, sf *stageFile, do func(io.Reader) (*drive.File, error)) (*drive.File, error) {
	var res *drive.File
	err := retry(ctx, func() (err error) {
		res, err = do(io.NewSectionReader(sf.f, 0, 1<<62))
		return err
	})
	return res, err
}

func conflictFileName(name string, now time.Time) string {
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext) + " (conflict " + now.Format("20060102-150405") + ")" + ext
}

func driveTime(mt int64) string {
	if mt == 0 {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return time.Unix(0, mt).UTC().Format(time.RFC3339Nano)
}

func (w *streamWriter) setRemoteMTime(ctx context.Context, id string, mt int64) (*drive.File, error) {
	var res *drive.File
	err := retry(ctx, func() (err error) {
		res, err = w.srv.Files.Update(id, &drive.File{ModifiedTime: driveTime(mt)}).Fields(fileFields).Context(ctx).Do()
		return err
	})
	return res, err
}

func (w *streamWriter) SetMTime(ctx context.Context, p string, mt int64) error {
	if sf := w.lookupPending(p); sf != nil {
		sf.setMTime(mt)
		sf.mu.Lock()
		edited := sf.version > 0 || sf.isNew
		sf.mu.Unlock()
		if edited {
			return nil
		}
	}
	r := w.snap()
	if r == nil {
		return errStreamMissing
	}
	cur, ok := r.Files[p]
	if !ok {
		return errStreamMissing
	}
	if cur.MTime/int64(time.Millisecond) == mt/int64(time.Millisecond) {
		return nil
	}
	dir, name := splitPath(p)
	parent, err := w.parentID(r, dir)
	if err != nil {
		return err
	}
	res, err := w.setRemoteMTime(ctx, cur.ID, mt)
	if err != nil {
		return err
	}
	w.index.note(res, name, parent)
	w.changed()
	log.Printf("streaming: set the modified time of %s to %s", p, time.Unix(0, mt).Format(time.RFC3339))
	return nil
}

func (w *streamWriter) trashGuarded(ctx context.Context, id, wantMD5 string) error {
	if !w.deletes.allow(time.Now()) {
		return errStreamTooManyDeletes
	}
	err := w.trashRemote(ctx, id, wantMD5)
	if err != nil {
		w.deletes.refund()
	}
	return err
}

func (w *streamWriter) trashRemote(ctx context.Context, id, wantMD5 string) error {
	var cur *drive.File
	err := retryRead(ctx, func() (err error) {
		cur, err = w.srv.Files.Get(id).Fields("id,md5Checksum,trashed").Context(ctx).Do()
		return err
	})
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.Trashed {
		return nil
	}
	if wantMD5 != "" && cur.Md5Checksum != wantMD5 {
		return errStreamStale
	}
	return retry(ctx, func() error {
		_, err := w.srv.Files.Update(id, &drive.File{Trashed: true}).Fields("id").Context(ctx).Do()
		return err
	})
}

func (w *streamWriter) Remove(ctx context.Context, p string) error {
	if sf := w.lookupPending(p); sf != nil {
		_, id, _, isNew := sf.snapshot()
		if !isNew {
			if err := w.trashGuarded(ctx, id, ""); err != nil {
				return err
			}
			w.index.forget(id)
			w.changed()
		}
		w.discard(sf)
		return nil
	}
	r := w.snap()
	if r == nil {
		return errStreamMissing
	}
	if _, isDir := r.Folders[p]; isDir {
		return errStreamIsDir
	}
	cur, ok := r.Files[p]
	if !ok {
		return errStreamMissing
	}
	if err := w.trashGuarded(ctx, cur.ID, cur.MD5); err != nil {
		return err
	}
	w.index.forget(cur.ID)
	w.changed()
	log.Printf("streaming: moved %s to the Google Drive trash", p)
	return nil
}

func (w *streamWriter) liveEmpty(ctx context.Context, id string) (bool, error) {
	var page *drive.FileList
	err := retryRead(ctx, func() (err error) {
		page, err = w.srv.Files.List().Q("'" + id + "' in parents and trashed=false").PageSize(1).Fields("files(id)").Context(ctx).Do()
		return err
	})
	return err == nil && len(page.Files) == 0, err
}

func (w *streamWriter) Rmdir(ctx context.Context, p string) error {
	r := w.snap()
	if r == nil {
		return errStreamMissing
	}
	id, ok := r.Folders[p]
	if !ok {
		return errStreamMissing
	}
	if kids, _ := (&streamTree{}).List(r, p); len(kids) > 0 || len(w.PendingIn(p)) > 0 {
		return errStreamNotEmpty
	}
	empty, err := w.liveEmpty(ctx, id)
	if err != nil {
		return err
	}
	if !empty {
		return errStreamNotEmpty
	}
	if err := w.trashGuarded(ctx, id, ""); err != nil {
		return err
	}
	w.index.forgetTree(id)
	w.changed()
	log.Printf("streaming: moved folder %s to the Google Drive trash", p)
	return nil
}

func (w *streamWriter) Mkdir(ctx context.Context, p string) error {
	dir, name := splitPath(p)
	if !validName(name) {
		return errStreamBadName
	}
	r := w.snap()
	if r == nil || !w.dirExists(r, dir) {
		return errStreamMissing
	}
	if _, ok := r.Folders[p]; ok {
		return errStreamExists
	}
	if _, ok := r.Files[p]; ok || w.lookupPending(p) != nil || w.hidden(r, p) {
		return errStreamExists
	}
	parent, err := w.parentID(r, dir)
	if err != nil {
		return err
	}
	idKey := "stream-dir:" + p
	id, err := w.ids.newID(ctx, w.srv, idKey)
	if err != nil {
		return err
	}
	err = retry(ctx, func() error {
		_, err := w.srv.Files.Create(&drive.File{Id: id, Name: name, MimeType: folderMime, Parents: []string{parent}}).Fields("id").Context(ctx).Do()
		if alreadyCreated(err) {
			_, err = adoptCreated(ctx, w.srv, id, "")
		}
		return err
	})
	if err != nil {
		return err
	}
	w.ids.releaseID(idKey)
	w.index.noteFolder(id, name, parent)
	w.changed()
	log.Printf("streaming: created folder %s on Google Drive", p)
	return nil
}

func (w *streamWriter) Rename(ctx context.Context, from, to string) error {
	if from == to {
		return nil
	}
	toDir, toName := splitPath(to)
	if !validName(toName) {
		return errStreamBadName
	}
	r := w.snap()
	if r == nil || !w.dirExists(r, toDir) {
		return errStreamMissing
	}
	if _, isDir := r.Folders[to]; isDir || (w.lookupPending(to) == nil && w.hidden(r, to)) {
		return errStreamExists
	}

	if sf := w.lookupPending(from); sf != nil {
		return w.renamePending(ctx, sf, from, to, r)
	}

	var id string
	isFolder := false
	if f, ok := r.Files[from]; ok {
		id = f.ID
	} else if fid, ok := r.Folders[from]; ok {
		id, isFolder = fid, true
	} else {
		return errStreamMissing
	}
	var target RemoteFile
	hasTarget := false
	_, targetIsFile := r.Files[to]
	if isFolder && (targetIsFile || w.lookupPending(to) != nil) {
		return errStreamExists
	}
	if t, ok := r.Files[to]; ok && !isFolder {
		target, hasTarget = t, true
	}
	tp := w.lookupPending(to)
	if tp != nil {
		_, tid, _, tnew := tp.snapshot()
		if !tnew {
			target, hasTarget = RemoteFile{ID: tid}, true
		}
	}
	fromDir, _ := splitPath(from)
	oldParent, err := w.parentID(r, fromDir)
	if err != nil {
		return err
	}
	newParent, err := w.parentID(r, toDir)
	if err != nil {
		return err
	}
	err = retry(ctx, func() error {
		call := w.srv.Files.Update(id, &drive.File{Name: toName}).Fields("id").Context(ctx)
		if oldParent != newParent {
			call = call.AddParents(newParent).RemoveParents(oldParent)
		}
		_, err := call.Do()
		return err
	})
	if err != nil {
		return err
	}
	w.index.moved(id, toName, newParent)
	if hasTarget && target.ID != id {
		if err := w.trashGuarded(ctx, target.ID, target.MD5); err != nil {
			log.Printf("replacing %s: the old file could not be moved to the trash: %v", to, err)
		} else {
			w.index.forget(target.ID)
		}
	}
	if tp != nil {
		w.discard(tp)
	}
	if !isFolder {
		if now := w.snap(); now != nil {
			if other, ok := now.Files[to]; ok && other.ID != id {
				if err := w.trashGuarded(ctx, other.ID, other.MD5); err != nil {
					log.Printf("replacing %s: the file that appeared there could not be moved to the trash: %v", to, err)
				} else {
					w.index.forget(other.ID)
				}
			}
		}
	}
	w.changed()
	if isFolder {
		w.movePendingUnder(from, to)
	}
	return nil
}

func (w *streamWriter) renamePending(ctx context.Context, sf *stageFile, from, to string, r *Remote) error {
	sf.mu.Lock()
	done, isNew, baseID := sf.done, sf.isNew, sf.baseID
	sf.mu.Unlock()
	if done {
		return errStreamStale
	}
	toDir, toName := splitPath(to)
	fromDir, _ := splitPath(from)

	var target RemoteFile
	hasTarget := false
	targetPending := false
	tp := w.lookupPending(to)
	if tp == sf {
		tp = nil
	}
	pendingSum := ""
	if tp != nil {
		_, tid, tsum, tnew := tp.snapshot()
		if !tnew {
			target, hasTarget, targetPending = RemoteFile{ID: tid, MD5: tsum}, true, true
			if s, _, err := fileMD5Of(tp.f); err == nil {
				pendingSum = s
			} else {
				log.Printf("streaming: reading the pending copy of %s before replacing it: %v", to, err)
			}
		}
	} else if t, ok := r.Files[to]; ok {
		target, hasTarget = t, true
	}
	trashMD5 := target.MD5
	if targetPending {
		trashMD5 = ""
	}

	if !isNew {
		oldParent, err := w.parentID(r, fromDir)
		if err != nil {
			return err
		}
		newParent, err := w.parentID(r, toDir)
		if err != nil {
			return err
		}
		err = retry(ctx, func() error {
			call := w.srv.Files.Update(baseID, &drive.File{Name: toName}).Fields("id").Context(ctx)
			if oldParent != newParent {
				call = call.AddParents(newParent).RemoveParents(oldParent)
			}
			_, err := call.Do()
			return err
		})
		if err != nil {
			return err
		}
		w.index.moved(baseID, toName, newParent)
		if hasTarget && target.ID != baseID {
			if err := w.trashGuarded(ctx, target.ID, trashMD5); err != nil {
				log.Printf("replacing %s: the old file could not be moved to the trash: %v", to, err)
			} else {
				w.index.forget(target.ID)
			}
		}
		w.changed()
	} else if hasTarget {
		sf.mu.Lock()
		running := sf.running
		sf.replace = target
		sf.mu.Unlock()
		if !running {
			var live *drive.File
			err := retryRead(ctx, func() (err error) {
				live, err = w.srv.Files.Get(target.ID).Fields("id,md5Checksum,trashed").Context(ctx).Do()
				return err
			})
			if err != nil {
				return err
			}
			sf.mu.Lock()
			if !sf.running && !live.Trashed {
				sf.baseID, sf.baseMD5, sf.isNew = target.ID, live.Md5Checksum, false
				sf.replace = RemoteFile{}
				sf.version++
				if pendingSum != "" {
					sf.sentMD5 = pendingSum
				}
			}
			sf.mu.Unlock()
		}
	}
	if tp != nil {
		w.discard(tp)
	}

	sf.mu.Lock()
	if sf.done {
		sf.mu.Unlock()
		return errStreamStale
	}
	sf.path = to
	sf.mu.Unlock()
	w.mu.Lock()
	if w.files[from] == sf {
		delete(w.files, from)
	}
	w.files[to] = sf
	w.mu.Unlock()
	return w.saveMeta(sf)
}

func (w *streamWriter) movePendingUnder(from, to string) {
	var moved []*stageFile
	w.mu.Lock()
	for p, sf := range w.files {
		if rest, ok := strings.CutPrefix(p, from+"/"); ok {
			delete(w.files, p)
			sf.mu.Lock()
			sf.path = to + "/" + rest
			sf.mu.Unlock()
			w.files[to+"/"+rest] = sf
			moved = append(moved, sf)
		}
	}
	w.mu.Unlock()
	for _, sf := range moved {
		w.saveMeta(sf)
	}
}
