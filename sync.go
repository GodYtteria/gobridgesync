package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

type Syncer struct {
	srv     *drive.Service
	paths   Paths
	st      *State
	remote  *Remote
	force   bool
	notify  func(string)
	stop    func() bool
	index   *driveIndex
	exclude *excluder
	limiter *limiter
	resumer *resumer
}

func (s *Syncer) save() error { return s.st.save(s.paths.state()) }

func (s *Syncer) abs(p string) string { return filepath.Join(s.paths.Root, filepath.FromSlash(p)) }

func (s *Syncer) Run(ctx context.Context) error {
	if _, err := os.Stat(s.paths.Root); err != nil {
		if !errors.Is(err, fs.ErrNotExist) || len(s.st.Files) > 0 {
			return fmt.Errorf("sync folder unavailable: %w", err)
		}
		if err := os.MkdirAll(s.paths.Root, 0o755); err != nil {
			return err
		}
	}
	remote, err := s.index.remoteOrList(ctx, s.srv)
	if err != nil {
		return fmt.Errorf("list drive: %w", err)
	}
	if prefix := syncPrefix(); prefix != "" {
		if remote.blocked(prefix) {
			return fmt.Errorf("more than one %q folder in My Drive; merge or rename the extras", prefix)
		}
		id, err := remote.ensureFolder(ctx, s.srv, prefix)
		if err != nil {
			return fmt.Errorf("create %s folder in Drive: %w", prefix, err)
		}
		remote = remote.sub(prefix, id)
	} else {
		excludeBackup(remote)
	}
	s.remote = remote
	if len(remote.Files) == 0 && len(s.st.Files) > 0 && !s.force {
		return errors.New("drive listing is empty but state is not; refusing (use -force)")
	}
	local, err := scanLocal(s.paths.Root, s.exclude)
	if err != nil {
		return fmt.Errorf("scan local: %w", err)
	}

	all := map[string]bool{}
	for p := range s.st.Files {
		all[p] = true
	}
	for p := range local {
		all[p] = true
	}
	for p := range remote.Files {
		all[p] = true
	}
	keys := make([]string, 0, len(all))
	for p := range all {
		if !remote.blocked(p) && !s.exclude.skip(s.abs(p), p) {
			keys = append(keys, p)
		}
	}
	sort.Strings(keys)

	plan := map[string]Action{}
	deletes := 0
	for _, p := range keys {
		var b *Entry
		var l *LocalFile
		var r *RemoteFile
		if v, ok := s.st.Files[p]; ok {
			b = &v
		}
		if v, ok := local[p]; ok {
			l = &v
		}
		if v, ok := remote.Files[p]; ok {
			r = &v
		}
		a := decide(b, l, r)
		plan[p] = a
		if a == DeleteLocal || a == DeleteRemote {
			deletes++
		}
	}
	if massDelete(deletes, len(s.st.Files)) && !s.force {
		return fmt.Errorf("%d deletions out of %d known files; refusing (use -force)", deletes, len(s.st.Files))
	}

	var errs []error
	budget := diskBudget(s.paths.Root)
	var skipped int
	var skippedBytes int64
	for _, p := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.stop != nil && s.stop() {
			return errPaused
		}
		a := plan[p]
		if a == Noop {
			continue
		}
		if a == Download || a == Compare {
			need := remote.Files[p].Size
			if need > budget {
				skipped++
				skippedBytes += need
				continue
			}
			budget -= need
		}
		if a != Forget {
			log.Printf("%s %s", a, p)
			if s.notify != nil {
				s.notify(a.String() + " " + p)
			}
		}
		lf := local[p]
		rf := remote.Files[p]
		if err := s.apply(ctx, a, p, lf, rf); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", a, p, err))
		}
	}
	if skipped > 0 {
		errs = append(errs, fmt.Errorf("not enough disk space: %d file(s) (%s) not downloaded; free up space on this computer", skipped, humanBytes(skippedBytes)))
	}
	if err := s.syncLinks(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Syncer) apply(ctx context.Context, a Action, p string, l LocalFile, r RemoteFile) error {
	switch a {
	case Download:
		return s.download(ctx, p, r)
	case Upload:
		var target *RemoteFile
		if r.ID != "" {
			target = &r
		}
		return s.upload(ctx, p, target)
	case DeleteLocal:
		return s.deleteLocal(p)
	case DeleteRemote:
		return s.deleteRemote(ctx, p)
	case Forget:
		delete(s.st.Files, p)
	case Compare:
		sum, err := fileMD5(s.abs(p))
		if err != nil {
			return err
		}
		if sum == r.MD5 {
			s.st.Files[p] = Entry{ID: r.ID, MD5: r.MD5, Size: l.Size, MTime: l.MTime}
			return nil
		}
		dst := conflictName(s.abs(p), time.Now())
		log.Printf("conflict %s: local copy kept as %s", p, filepath.Base(dst))
		if err := os.Rename(s.abs(p), dst); err != nil {
			return err
		}
		return s.download(ctx, p, r)
	}
	return nil
}

func conflictName(full string, now time.Time) string {
	ext := filepath.Ext(full)
	return strings.TrimSuffix(full, ext) + " (conflict " + now.Format("20060102-150405") + ")" + ext
}

func (s *Syncer) record(p string, id, sum string) error {
	info, err := os.Stat(s.abs(p))
	if err != nil {
		return err
	}
	s.st.Files[p] = Entry{ID: id, MD5: sum, Size: info.Size(), MTime: info.ModTime().UnixNano()}
	return nil
}

func (s *Syncer) download(ctx context.Context, p string, r RemoteFile) error {
	if err := downloadFile(ctx, s.srv, r.ID, "", r.MD5, r.MTime, s.abs(p), s.limiter); err != nil {
		return err
	}
	return s.record(p, r.ID, r.MD5)
}

func (s *Syncer) upload(ctx context.Context, p string, r *RemoteFile) error {
	full := s.abs(p)
	if r != nil {
		sum, err := fileMD5(full)
		if err != nil {
			return err
		}
		if sum == r.MD5 {
			return s.record(p, r.ID, r.MD5)
		}
	}
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	meta := &drive.File{ModifiedTime: info.ModTime().UTC().Format(time.RFC3339Nano)}
	var res *drive.File
	if s.resumer != nil && info.Size() >= resumableMin {
		lf := LocalFile{Size: info.Size(), MTime: info.ModTime().UnixNano()}
		t := uploadTarget{key: "sync:" + sessionKey(p, lf), name: path.Base(p), mtime: info.ModTime()}
		if r != nil {
			t.fileID = r.ID
		} else if t.parent, err = s.remote.ensureFolder(ctx, s.srv, path.Dir(p)); err != nil {
			return err
		}
		res, err = s.resumer.upload(ctx, t, full, info.Size(), s.limiter, nil)
	} else if r != nil {
		res, err = s.srv.Files.Update(r.ID, meta).Media(limitReader(ctx, f, s.limiter), googleapi.ChunkSize(uploadChunk)).Fields(fileFields).Context(ctx).Do()
	} else {
		parent, perr := s.remote.ensureFolder(ctx, s.srv, path.Dir(p))
		if perr != nil {
			return perr
		}
		meta.Name = path.Base(p)
		meta.Parents = []string{parent}
		res, err = s.srv.Files.Create(meta).Media(limitReader(ctx, f, s.limiter), googleapi.ChunkSize(uploadChunk)).Fields(fileFields).Context(ctx).Do()
	}
	if err != nil {
		return err
	}
	s.st.Files[p] = Entry{ID: res.Id, MD5: res.Md5Checksum, Size: info.Size(), MTime: info.ModTime().UnixNano()}
	return nil
}

func (r *Remote) ensureFolder(ctx context.Context, srv *drive.Service, dir string) (string, error) {
	if dir == "." {
		dir = ""
	}
	if id, ok := r.Folders[dir]; ok {
		return id, nil
	}
	parent, err := r.ensureFolder(ctx, srv, path.Dir(dir))
	if err != nil {
		return "", err
	}
	f, err := srv.Files.Create(&drive.File{Name: path.Base(dir), MimeType: folderMime, Parents: []string{parent}}).
		Fields("id").Context(ctx).Do()
	if err != nil {
		return "", err
	}
	r.Folders[dir] = f.Id
	return f.Id, nil
}

func (s *Syncer) deleteLocal(p string) error {
	dst := filepath.Join(s.paths.trash(), time.Now().Format("20060102-150405"), filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(s.abs(p), dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	delete(s.st.Files, p)
	return nil
}

func (s *Syncer) deleteRemote(ctx context.Context, p string) error {
	_, err := s.srv.Files.Update(s.st.Files[p].ID, &drive.File{Trashed: true}).Fields("id").Context(ctx).Do()
	var gerr *googleapi.Error
	if err != nil && !(errors.As(err, &gerr) && gerr.Code == http.StatusNotFound) {
		return err
	}
	delete(s.st.Files, p)
	return nil
}

func (s *Syncer) syncLinks() error {
	var errs []error
	for p, d := range s.remote.Docs {
		b, err := json.Marshal(map[string]string{"url": d.URL, "doc_id": d.ID})
		if err != nil {
			return err
		}
		full := s.abs(p)
		if cur, err := os.ReadFile(full); err != nil || string(cur) != string(b) {
			if err := writeFileAtomic(full, b, 0o644); err != nil {
				errs = append(errs, fmt.Errorf("link %s: %w", p, err))
				continue
			}
		}
		s.st.Links[p] = d.ID
	}
	for p := range s.st.Links {
		if _, ok := s.remote.Docs[p]; ok || s.remote.blocked(p) {
			continue
		}
		if err := os.Remove(s.abs(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("link %s: %w", p, err))
			continue
		}
		delete(s.st.Links, p)
	}
	return errors.Join(errs...)
}

const diskReserve = 2 << 30

func diskBudget(dir string) int64 {
	free, err := freeSpace(dir)
	if err != nil {
		return math.MaxInt64
	}
	return max(free-diskReserve, 0)
}

func excludeBackup(r *Remote) {
	prefix := backupRootName + "/"
	for p := range r.Files {
		if strings.HasPrefix(p, prefix) {
			delete(r.Files, p)
		}
	}
	for p := range r.Docs {
		if strings.HasPrefix(p, prefix) {
			delete(r.Docs, p)
		}
	}
	r.Ambiguous[foldKey(backupRootName)] = true
}
