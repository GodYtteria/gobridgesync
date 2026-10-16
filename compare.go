package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func conflictName(full string, now time.Time) string {
	ext := filepath.Ext(full)
	base := strings.TrimSuffix(full, ext) + " (conflict " + now.Format("20060102-150405")
	name := base + ")" + ext
	for n := 2; ; n++ {
		if _, err := os.Lstat(name); err != nil {
			return name
		}
		name = fmt.Sprintf("%s %d)%s", base, n, ext)
	}
}

type pendingCompare struct {
	path   string
	local  LocalFile
	remote RemoteFile
}

func (s *Syncer) compareAll(ctx context.Context, jobs []pendingCompare) ([]pendingCompare, []error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	prog := Progress{Phase: "checking", FilesTotal: len(jobs)}
	for _, j := range jobs {
		prog.BytesTotal += j.local.Size
	}
	ch := make(chan pendingCompare)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conflicts []pendingCompare
	var errs []error
	for range uploadWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				same := false
				var err error
				if j.local.Size == j.remote.Size {
					var sum string
					if sum, err = fileMD5(s.abs(j.path)); err == nil {
						same = sum == j.remote.MD5
					}
				}
				mu.Lock()
				switch {
				case err != nil:
					errs = append(errs, fmt.Errorf("check %s: %w", j.path, err))
				case same:
					s.st.Files[j.path] = Entry{ID: j.remote.ID, MD5: j.remote.MD5, Size: j.local.Size, MTime: j.local.MTime}
				default:
					conflicts = append(conflicts, j)
				}
				prog.FilesDone++
				prog.BytesDone += j.local.Size
				if s.progress != nil {
					s.progress(prog)
				}
				mu.Unlock()
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
	return conflicts, errs
}
