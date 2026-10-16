package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

func (s *Syncer) deleteRemote(ctx context.Context, p string) error {
	s.mu.Lock()
	id := s.st.Files[p].ID
	s.mu.Unlock()
	s.mu.Lock()
	known := s.st.Files[p].MD5
	s.mu.Unlock()
	var cur *drive.File
	gerr := new(googleapi.Error)
	err := retryRead(ctx, func() (err error) {
		cur, err = s.srv.Files.Get(id).Fields("id,md5Checksum,modifiedTime,size,trashed").Context(ctx).Do()
		return err
	})
	switch {
	case errors.As(err, &gerr) && gerr.Code == http.StatusNotFound:
	case err != nil:
		return err
	case !cur.Trashed && cur.Md5Checksum != known:
		s.index.note(cur, path.Base(p), "")
		log.Printf("delete %s: changed on Drive in the meantime; keeping it", p)
		if s.more != nil && s.index != nil {
			s.more()
		}
		return nil
	case !cur.Trashed:
		err = retry(ctx, func() error {
			_, err := s.srv.Files.Update(id, &drive.File{Trashed: true}).Fields("id").Context(ctx).Do()
			return err
		})
		if err != nil && !(errors.As(err, &gerr) && gerr.Code == http.StatusNotFound) {
			return err
		}
	}
	s.index.forget(id)
	s.mu.Lock()
	delete(s.st.Files, p)
	s.mu.Unlock()
	return nil
}

func emptyFolders(remote *Remote, deleted map[string]bool, localDirExists func(string) bool) []string {
	cands := map[string]bool{}
	for p := range deleted {
		for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
			cands[d] = true
		}
	}
	for d := range cands {
		if _, ok := remote.Folders[d]; !ok || localDirExists(d) {
			delete(cands, d)
		}
	}
	busy := func(p string) {
		for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
			delete(cands, d)
		}
	}
	for p := range remote.Files {
		if !deleted[p] {
			busy(p)
		}
	}
	for p := range remote.Docs {
		busy(p)
	}
	for p := range remote.Folders {
		if !cands[p] {
			busy(p)
		}
	}
	var top []string
	for d := range cands {
		if !cands[path.Dir(d)] {
			top = append(top, d)
		}
	}
	sort.Strings(top)
	return top
}

func (s *Syncer) trashEmptyFolders(ctx context.Context, remote *Remote, tried []string) []error {
	deleted := map[string]bool{}
	trashedIDs := map[string]bool{}
	s.mu.Lock()
	for _, p := range tried {
		if _, ok := s.st.Files[p]; !ok {
			deleted[p] = true
			trashedIDs[remote.Files[p].ID] = true
		}
	}
	s.mu.Unlock()
	if len(deleted) == 0 {
		return nil
	}
	var errs []error
	for _, d := range emptyFolders(remote, deleted, func(d string) bool {
		info, err := os.Stat(s.abs(d))
		return err == nil && info.IsDir()
	}) {
		id := remote.Folders[d]
		empty, err := s.driveFolderEmpty(ctx, id, trashedIDs)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", DeleteRemote, d, err))
			continue
		}
		if !empty {
			continue
		}
		if !s.folderUnchanged(ctx, id) {
			continue
		}
		err = retry(ctx, func() error {
			_, err := s.srv.Files.Update(id, &drive.File{Trashed: true}).Fields("id").Context(ctx).Do()
			return err
		})
		var gerr *googleapi.Error
		if err != nil && !(errors.As(err, &gerr) && gerr.Code == http.StatusNotFound) {
			errs = append(errs, fmt.Errorf("%s %s: %w", DeleteRemote, d, err))
			continue
		}
		s.index.forgetTree(id)
		s.report("delete-remote-folder " + d)
	}
	return errs
}

func (s *Syncer) folderUnchanged(ctx context.Context, id string) bool {
	want := s.index.modTime(id)
	if want == "" {
		return false
	}
	var cur *drive.File
	err := retryRead(ctx, func() (err error) {
		cur, err = s.srv.Files.Get(id).Fields("id,modifiedTime,trashed").Context(ctx).Do()
		return err
	})
	return err == nil && !cur.Trashed && cur.ModifiedTime == want
}

func (s *Syncer) driveFolderEmpty(ctx context.Context, id string, ignore map[string]bool) (bool, error) {
	level := []string{id}
	for depth := 0; len(level) > 0; depth++ {
		if depth > maxFolderDepth {
			return false, nil
		}
		var next []string
		for chunk := range slices.Chunk(level, folderQueryBatch) {
			q := make([]string, len(chunk))
			for i, c := range chunk {
				q[i] = "'" + c + "' in parents"
			}
			token := ""
			for {
				var page *drive.FileList
				err := retryRead(ctx, func() (err error) {
					page, err = s.srv.Files.List().Q("(" + strings.Join(q, " or ") + ") and trashed=false").PageSize(1000).PageToken(token).Fields("nextPageToken, files(id, mimeType)").Context(ctx).Do()
					return err
				})
				if err != nil {
					return false, err
				}
				for _, f := range page.Files {
					if ignore[f.Id] {
						continue
					}
					if f.MimeType != folderMime {
						return false, nil
					}
					next = append(next, f.Id)
				}
				if page.NextPageToken == "" {
					break
				}
				token = page.NextPageToken
			}
		}
		level = next
	}
	return true, nil
}

func (s *Syncer) deleteRemoteAll(ctx context.Context, paths []string) []error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	ch := make(chan string)
	for range min(deleteWorkers, len(paths)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range ch {
				if err := retry(ctx, func() error { return s.deleteRemote(ctx, p) }); err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("%s %s: %w", DeleteRemote, p, err))
					mu.Unlock()
				}
			}
		}()
	}
	paused := false
	for _, p := range paths {
		if ctx.Err() != nil {
			break
		}
		if s.stop != nil && s.stop() {
			paused = true
			break
		}
		ch <- p
	}
	close(ch)
	wg.Wait()
	if paused {
		errs = append(errs, errPaused)
	}
	return errs
}
