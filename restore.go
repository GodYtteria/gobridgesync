package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
)

const defaultRestoreDir = "~/GoBridgeSync Restore"

type RemoteEntry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	ID       string    `json:"id"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type Version struct {
	ID       string    `json:"id"`
	Modified time.Time `json:"modified"`
	Size     int64     `json:"size"`
	MD5      string    `json:"md5"`
}

type restoreJob struct {
	Source   string `json:"source"`
	FileID   string `json:"fileId"`
	Revision string `json:"revision"`
	Name     string `json:"name"`
	Dest     string `json:"dest"`
}

type restoreItem struct {
	id, revision, sum, rel string
	size, mtime            int64
}

func parentDir(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

func browseRemote(rem *Remote, dir string) []RemoteEntry {
	out := []RemoteEntry{}
	for p, id := range rem.Folders {
		if p != "" && parentDir(p) == dir {
			out = append(out, RemoteEntry{Name: path.Base(p), Path: p, ID: id, Dir: true})
		}
	}
	for p, f := range rem.Files {
		if parentDir(p) == dir {
			out = append(out, RemoteEntry{Name: path.Base(p), Path: p, ID: f.ID, Size: f.Size, Modified: time.Unix(0, f.MTime)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func restoreItems(rem *Remote, source string) []restoreItem {
	var out []restoreItem
	for p, f := range rem.Files {
		rel := ""
		switch {
		case p == source:
			rel = path.Base(p)
		case source == "" || strings.HasPrefix(p, source+"/"):
			rel = strings.TrimPrefix(p, source+"/")
		default:
			continue
		}
		out = append(out, restoreItem{id: f.ID, sum: f.MD5, rel: rel, size: f.Size, mtime: f.MTime})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

func restoreFolders(rem *Remote, source string) []string {
	var out []string
	for p := range rem.Folders {
		if source != "" && strings.HasPrefix(p, source+"/") {
			out = append(out, strings.TrimPrefix(p, source+"/"))
		}
	}
	sort.Strings(out)
	return out
}

func freeName(target string) (string, error) {
	ext := filepath.Ext(target)
	base := strings.TrimSuffix(target, ext)
	for n := 1; n <= 1000; n++ {
		suffix := " (restored)"
		if n > 1 {
			suffix = fmt.Sprintf(" (restored %d)", n)
		}
		candidate := base + suffix + ext
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("too many restored copies")
}

func restoreDest(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = defaultRestoreDir
	}
	dir, err := expandHome(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("restore to a full path like ~/Restore")
	}
	return filepath.Clean(dir), nil
}

func (r *Runner) Browse(dir string) ([]RemoteEntry, error) {
	rem := r.snapshot.Load()
	if rem == nil {
		r.Trigger()
		return nil, errors.New("still loading your backups, try again in a moment")
	}
	return browseRemote(rem, strings.Trim(dir, "/")), nil
}

func (r *Runner) Versions(ctx context.Context, fileID string) ([]Version, error) {
	srv := r.srv.Load()
	if srv == nil {
		return nil, errors.New("not connected to Google Drive yet")
	}
	list, err := srv.Revisions.List(fileID).Fields("revisions(id,modifiedTime,size,md5Checksum)").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(list.Revisions))
	for _, v := range list.Revisions {
		t, _ := time.Parse(time.RFC3339, v.ModifiedTime)
		out = append(out, Version{ID: v.Id, Modified: t, Size: v.Size, MD5: v.Md5Checksum})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

func (r *Runner) QueueRestore(job restoreJob) error {
	if _, err := restoreDest(job.Dest); err != nil {
		return err
	}
	if job.FileID == "" && job.Source == "" {
		return errors.New("choose what to restore")
	}
	if job.FileID != "" && !validName(job.Name) {
		return errors.New("invalid file name")
	}
	select {
	case r.jobs <- job:
		r.addRecent("restore queued")
		return nil
	default:
		return errors.New("a restore is already waiting; try again when it finishes")
	}
}

func (r *Runner) runRestore(ctx context.Context, srv *drive.Service, job restoreJob) error {
	var lim *limiter
	if cfg, err := loadAppConfig(r.paths); err == nil {
		lim = cfg.limiter()
	}
	dest, err := restoreDest(job.Dest)
	if err != nil {
		return err
	}
	var items []restoreItem
	if job.FileID != "" {
		item := restoreItem{id: job.FileID, revision: job.Revision, rel: job.Name}
		if job.Revision != "" {
			v, err := srv.Revisions.Get(job.FileID, job.Revision).Fields("md5Checksum,size,modifiedTime").Context(ctx).Do()
			if err != nil {
				return err
			}
			t, _ := time.Parse(time.RFC3339, v.ModifiedTime)
			item.sum, item.size, item.mtime = v.Md5Checksum, v.Size, t.UnixNano()
			ext := path.Ext(job.Name)
			item.rel = strings.TrimSuffix(job.Name, ext) + " (" + t.Local().Format("2006-01-02 1504") + ")" + ext
		} else if rem := r.snapshot.Load(); rem != nil {
			for _, f := range rem.Files {
				if f.ID == job.FileID {
					item.sum, item.size, item.mtime = f.MD5, f.Size, f.MTime
				}
			}
		}
		items = []restoreItem{item}
	} else {
		rem := r.snapshot.Load()
		if rem == nil {
			return errors.New("backups are not loaded yet")
		}
		source := strings.Trim(job.Source, "/")
		items = restoreItems(rem, source)
		for _, d := range restoreFolders(rem, source) {
			target := filepath.Join(dest, filepath.FromSlash(d))
			if within(target, dest) {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
			}
		}
	}
	if len(items) == 0 {
		return errors.New("nothing to restore there")
	}
	var total int64
	for _, it := range items {
		total += it.size
	}
	r.set(func(s *Status) {
		s.State, s.Message = "syncing", "Restoring"
		s.Progress = Progress{Phase: "restoring", FilesTotal: len(items), BytesTotal: total}
	})

	cleaned := map[string]bool{}
	for _, it := range items {
		dir := filepath.Dir(filepath.Join(dest, filepath.FromSlash(it.rel)))
		if within(dir, dest) && !cleaned[dir] {
			cleaned[dir] = true
			cleanStaleParts(dir)
		}
	}

	ch := make(chan restoreItem)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	restored := 0
	for range uploadWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				done, err := restoreOne(ctx, srv, it, dest, lim)
				mu.Lock()
				if err != nil {
					errs = append(errs, fmt.Errorf("restore %s: %w", it.rel, err))
				} else if done {
					restored++
				}
				mu.Unlock()
				r.set(func(s *Status) {
					s.Progress.FilesDone++
					s.Progress.BytesDone += it.size
				})
			}
		}()
	}
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		ch <- it
	}
	close(ch)
	wg.Wait()
	r.addRecent(fmt.Sprintf("restored %d file(s) to %s", restored, dest))
	return errors.Join(errs...)
}

func restoreOne(ctx context.Context, srv *drive.Service, it restoreItem, dest string, lim *limiter) (bool, error) {
	target := filepath.Join(dest, filepath.FromSlash(it.rel))
	if !within(target, dest) {
		return false, errors.New("unsafe path")
	}
	if info, err := os.Stat(target); err == nil {
		if info.Size() == it.size && it.sum != "" {
			if sum, err := fileMD5(target); err == nil && sum == it.sum {
				return false, nil
			}
		}
		free, err := freeName(target)
		if err != nil {
			return false, err
		}
		target = free
	}
	return true, retry(ctx, func() error {
		return downloadFile(ctx, srv, it.id, it.revision, it.sum, it.mtime, target, lim)
	})
}
