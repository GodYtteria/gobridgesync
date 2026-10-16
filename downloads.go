package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/api/googleapi"
)

type scanView struct {
	file    LocalFile
	present bool
}

type pendingDownload struct {
	path string
	file RemoteFile
	seen scanView
}

func notDownloadable(err error) bool {
	var g *googleapi.Error
	if !errors.As(err, &g) || g.Code != http.StatusForbidden {
		return false
	}
	for _, e := range g.Errors {
		if e.Reason == "cannotDownloadAbusiveFile" || e.Reason == "fileNotDownloadable" || e.Reason == "cannotDownloadFile" {
			return true
		}
	}
	return false
}

func (s *Syncer) downloadAll(ctx context.Context, jobs []pendingDownload) []error {
	if len(jobs) == 0 {
		return nil
	}
	var total int64
	for _, j := range jobs {
		total += j.file.Size
	}
	prog := Progress{Phase: "downloading", FilesTotal: len(jobs), BytesTotal: total}
	report := func() {
		if s.progress != nil {
			s.progress(prog)
		}
	}
	report()
	ch := make(chan pendingDownload)
	var wg sync.WaitGroup
	var errs []error
	for range uploadWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				s.report("download " + j.path)
				err := retryRead(ctx, func() error { return s.download(ctx, j.path, j.file, &j.seen) })
				s.mu.Lock()
				switch {
				case notDownloadable(err):
					s.st.Unavailable[j.path] = j.file.MD5
					errs = append(errs, fmt.Errorf("download %s: Google does not allow downloading this file; skipped until it changes", j.path))
				case err != nil:
					errs = append(errs, fmt.Errorf("download %s: %w", j.path, err))
				default:
					delete(s.st.Unavailable, j.path)
				}
				prog.FilesDone++
				prog.BytesDone += j.file.Size
				if err == nil {
					prog.Downloaded++
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

func (s *Syncer) download(ctx context.Context, p string, r RemoteFile, seen *scanView) error {
	place := os.Rename
	if seen != nil {
		place = func(part, full string) error { return placeDownload(p, part, full, *seen) }
	}
	if err := downloadFileTo(ctx, s.srv, r.ID, "", r.MD5, r.MTime, s.abs(p), s.limiter, place); err != nil {
		return err
	}
	return s.record(p, r.ID, r.MD5)
}

func placeDownload(p, part, full string, seen scanView) error {
	info, err := os.Lstat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case !seen.present || info.Size() != seen.file.Size || info.ModTime().UnixNano() != seen.file.MTime:
		dst := conflictName(full, time.Now())
		log.Printf("conflict %s: changed on this computer during the sync; local copy kept as %s", p, filepath.Base(dst))
		if err := os.Rename(full, dst); err != nil {
			return err
		}
	}
	return os.Rename(part, full)
}

const diskReserve = 2 << 30

func diskBudget(dir string) int64 {
	free, err := freeSpace(dir)
	if err != nil {
		return math.MaxInt64
	}
	return max(free-diskReserve, 0)
}
