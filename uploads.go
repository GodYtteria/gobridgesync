package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

func (s *Syncer) record(p string, id, sum string) error {
	info, err := os.Stat(s.abs(p))
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.st.Files[p] = Entry{ID: id, MD5: sum, Size: info.Size(), MTime: info.ModTime().UnixNano()}
	s.mu.Unlock()
	return nil
}

func notFound(err error) bool {
	var g *googleapi.Error
	return errors.As(err, &g) && g.Code == http.StatusNotFound
}

func (s *Syncer) releaseID(p string) {
	log.Printf("upload %s: earlier Drive id no longer exists; using a new one", p)
	s.remote.releaseID("file:" + p)
}

func (s *Syncer) remoteMoved(ctx context.Context, p string, r RemoteFile) (bool, error) {
	var cur *drive.File
	err := retryRead(ctx, func() (err error) {
		cur, err = s.srv.Files.Get(r.ID).Fields("id,md5Checksum,modifiedTime,size,trashed").Context(ctx).Do()
		return err
	})
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == http.StatusNotFound {
		s.index.forget(r.ID)
		log.Printf("upload %s: no longer on Drive; checking again", p)
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if cur.Trashed {
		s.index.forget(r.ID)
		log.Printf("upload %s: deleted on Drive in the meantime; checking again", p)
		return true, nil
	}
	if cur.Md5Checksum == r.MD5 {
		return false, nil
	}
	if cur.Md5Checksum == "" {
		log.Printf("upload %s: replaced by a file type that cannot be synced; skipped", p)
		return true, nil
	}
	s.index.note(cur, path.Base(p), "")
	log.Printf("upload %s: changed on Drive in the meantime; checking again", p)
	if s.more != nil && s.index != nil {
		s.more()
	}
	return true, nil
}

func (s *Syncer) upload(ctx context.Context, p string, r *RemoteFile) error {
	return s.uploadTry(ctx, p, r, true)
}

func (s *Syncer) uploadTry(ctx context.Context, p string, r *RemoteFile, freshID bool) error {
	full := s.abs(p)
	if r != nil {
		sum, err := fileMD5(full)
		if err != nil {
			return err
		}
		if sum == r.MD5 {
			return s.record(p, r.ID, r.MD5)
		}
		changed, err := s.remoteMoved(ctx, p, *r)
		if err != nil || changed {
			return err
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
		} else if t.parent, err = s.folder(ctx, path.Dir(p)); err != nil {
			return err
		} else if t.newID, err = s.remote.newID(ctx, s.srv, "file:"+p); err != nil {
			return err
		}
		if t.parent != "" {
			meta.Parents = []string{t.parent}
		}
		res, err = s.resumer.upload(ctx, t, full, info.Size(), s.limiter, nil)
		if alreadyCreated(err) && t.newID != "" {
			sum, serr := fileMD5(full)
			if serr != nil {
				return serr
			}
			res, err = adoptCreated(ctx, s.srv, t.newID, sum)
			if errors.Is(err, errAdoptTrashed) {
				s.journal.drop("file:" + p)
			}
			if errors.Is(err, errAdoptStale) {
				return s.upload(ctx, p, &RemoteFile{ID: res.Id, MD5: res.Md5Checksum})
			}
			if freshID && notFound(err) {
				s.releaseID(p)
				return s.uploadTry(ctx, p, r, false)
			}
		}
	} else if r != nil {
		res, err = s.srv.Files.Update(r.ID, meta).Media(limitReader(ctx, f, s.limiter), googleapi.ChunkSize(uploadChunk)).Fields(fileFields).Context(ctx).Do()
	} else {
		parent, perr := s.folder(ctx, path.Dir(p))
		if perr != nil {
			return perr
		}
		id, ierr := s.remote.newID(ctx, s.srv, "file:"+p)
		if ierr != nil {
			return ierr
		}
		meta.Id, meta.Name, meta.Parents = id, path.Base(p), []string{parent}
		res, err = s.srv.Files.Create(meta).Media(limitReader(ctx, f, s.limiter), googleapi.ChunkSize(uploadChunk)).Fields(fileFields).Context(ctx).Do()
		if alreadyCreated(err) {
			sum, serr := fileMD5(full)
			if serr != nil {
				return serr
			}
			res, err = adoptCreated(ctx, s.srv, id, sum)
			if errors.Is(err, errAdoptTrashed) {
				s.journal.drop("file:" + p)
			}
			if errors.Is(err, errAdoptStale) {
				return s.upload(ctx, p, &RemoteFile{ID: res.Id, MD5: res.Md5Checksum})
			}
			if freshID && notFound(err) {
				s.releaseID(p)
				return s.uploadTry(ctx, p, r, false)
			}
		}
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.st.Files[p] = Entry{ID: res.Id, MD5: res.Md5Checksum, Size: info.Size(), MTime: info.ModTime().UnixNano()}
	s.mu.Unlock()
	parent := ""
	if len(meta.Parents) > 0 {
		parent = meta.Parents[0]
	}
	s.index.note(res, path.Base(p), parent)
	if now, serr := os.Stat(full); serr == nil && (now.Size() != info.Size() || !now.ModTime().Equal(info.ModTime())) {
		log.Printf("upload %s: changed while uploading; uploading again", p)
		if s.more != nil && !stillWriting(LocalFile{Size: now.Size(), MTime: now.ModTime().UnixNano()}, time.Now()) {
			s.more()
		}
	}
	return nil
}

const (
	uploadsPerPass   = 300
	busyFileSize     = 16 << 20
	busyFileQuiet    = 2 * time.Minute
	busyFileMaxAge   = 15 * time.Minute
	deleteWorkers    = 8
	maxFolderDepth   = 32
	folderQueryBatch = 50
)

func vanished(err error, full string) bool {
	if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	_, serr := os.Lstat(full)
	return errors.Is(serr, fs.ErrNotExist)
}

func stillWriting(lf LocalFile, now time.Time) bool {
	return lf.Size >= busyFileSize && now.Sub(time.Unix(0, lf.MTime)) < busyFileQuiet
}

func (s *Syncer) waitForQuiet(p string, lf LocalFile, now time.Time) bool {
	if !stillWriting(lf, now) {
		return false
	}
	s.mu.Lock()
	known, ok := s.st.Files[p]
	s.mu.Unlock()
	return !ok || now.Sub(time.Unix(0, known.MTime)) < busyFileMaxAge
}

func (s *Syncer) prioritize(jobs []pendingUpload, local map[string]LocalFile) []pendingUpload {
	sort.SliceStable(jobs, func(i, j int) bool { return local[jobs[i].path].MTime > local[jobs[j].path].MTime })
	if len(jobs) > uploadsPerPass {
		jobs = jobs[:uploadsPerPass]
		if s.more != nil {
			s.more()
		}
	}
	return jobs
}

type pendingUpload struct {
	path string
	file RemoteFile
}

func (s *Syncer) uploadAll(ctx context.Context, jobs []pendingUpload) []error {
	if len(jobs) == 0 {
		return nil
	}
	if s.pending != nil {
		tops := make([]string, 0, len(jobs))
		for _, j := range jobs {
			first, _, _ := strings.Cut(j.path, "/")
			tops = append(tops, s.abs(first))
		}
		s.pending(tops)
		defer s.pending(nil)
	}
	prog := Progress{Phase: "uploading", FilesTotal: len(jobs)}
	for _, j := range jobs {
		if info, err := os.Stat(s.abs(j.path)); err == nil {
			prog.BytesTotal += info.Size()
		}
	}
	report := func() {
		if s.progress != nil {
			s.progress(prog)
		}
	}
	report()
	ch := make(chan pendingUpload)
	var wg sync.WaitGroup
	var errs []error
	done := 0
	for range uploadWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				s.report("upload " + j.path)
				var target *RemoteFile
				if j.file.ID != "" {
					target = &j.file
				}
				err := retry(ctx, func() error { return s.upload(ctx, j.path, target) })
				gone := vanished(err, s.abs(j.path))
				if gone {
					log.Printf("upload %s: removed before it could be uploaded; skipped", j.path)
				}
				s.mu.Lock()
				switch {
				case gone:
				case err != nil:
					errs = append(errs, fmt.Errorf("upload %s: %w", j.path, err))
				default:
					prog.Uploaded++
				}
				prog.FilesDone++
				if info, serr := os.Stat(s.abs(j.path)); serr == nil {
					prog.BytesDone += info.Size()
				}
				if done++; done%saveEvery == 0 {
					if serr := s.st.save(s.paths.state()); serr != nil {
						errs = append(errs, serr)
					}
				}
				report()
				s.mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		if ctx.Err() != nil || (s.stop != nil && s.stop()) {
			break
		}
		ch <- j
	}
	close(ch)
	wg.Wait()
	return errs
}
