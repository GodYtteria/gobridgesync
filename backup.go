package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

const (
	backupRootName = "GoBridgeSync Backup"
	saveEvery      = 50
	maxScanErrors  = 20
	uploadWorkers  = 4
	uploadChunk    = 4 << 20
)

var errPaused = errors.New("paused")

type Progress struct {
	Phase      string `json:"phase"`
	Uploaded   int    `json:"uploaded"`
	FilesDone  int    `json:"filesDone"`
	FilesTotal int    `json:"filesTotal"`
	BytesDone  int64  `json:"bytesDone"`
	BytesTotal int64  `json:"bytesTotal"`
}

type FolderSize struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

func measureFiles(files map[string]LocalFile) FolderSize {
	var s FolderSize
	for _, f := range files {
		s.Files++
		s.Bytes += f.Size
	}
	return s
}

type Quota struct {
	Used  int64 `json:"used"`
	Limit int64 `json:"limit"`
}

type Backup struct {
	srv      *drive.Service
	folders  []string
	st       *State
	save     func() error
	notify   func(string)
	stop     func() bool
	limitPct int
	progress func(Progress)
	sizes    func(map[string]FolderSize)
	quota    func(Quota)

	mu       sync.Mutex
	folderMu sync.Mutex
	remote   *Remote
	prog     Progress
	index    *driveIndex
	exclude  *excluder
	limiter  *limiter
	resumer  *resumer
}

type uploadJob struct {
	full string
	rp   string
	lf   LocalFile
	r    *RemoteFile
}

func backupHost() string {
	h, err := os.Hostname()
	if err != nil || !validName(h) {
		return "computer"
	}
	return h
}

func backupTarget() string { return path.Join(backupRootName, backupHost()) }

func backupNames(folders []string) []string {
	used := map[string]int{}
	out := make([]string, len(folders))
	for i, f := range folders {
		name := filepath.Base(f)
		if !validName(name) {
			name = "folder"
		}
		used[foldKey(name)]++
		if n := used[foldKey(name)]; n > 1 {
			name = fmt.Sprintf("%s-%d", name, n)
		}
		out[i] = name
	}
	return out
}

func scanTree(root string, ex *excluder) (map[string]LocalFile, []string, []error) {
	out := map[string]LocalFile{}
	var dirs []string
	var errs []error
	note := func(err error) {
		if len(errs) < maxScanErrors {
			errs = append(errs, err)
		}
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			note(err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		name := d.Name()
		if d.IsDir() && !validName(name) {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			note(err)
			return nil
		}
		rel = filepath.ToSlash(rel)
		if ex.skip(p, rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, rel)
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(name, tmpPrefix) || !validName(name) {
			removeStalePart(p, d)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			note(err)
			return nil
		}
		out[rel] = LocalFile{Size: info.Size(), MTime: info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		return nil, nil, append(errs, err)
	}
	return out, dirs, errs
}

func emptyDirs(files map[string]LocalFile, dirs []string) []string {
	used := map[string]bool{}
	for rel := range files {
		for d := path.Dir(rel); d != "."; d = path.Dir(d) {
			if used[d] {
				break
			}
			used[d] = true
		}
	}
	out := []string{}
	if len(files) == 0 {
		out = append(out, ".")
	}
	for _, d := range dirs {
		if !used[d] && !hasChildDir(d, dirs) {
			out = append(out, d)
		}
	}
	return out
}

func hasChildDir(d string, dirs []string) bool {
	prefix := d + "/"
	for _, o := range dirs {
		if strings.HasPrefix(o, prefix) {
			return true
		}
	}
	return false
}

func (b *Backup) report(f func(*Progress)) {
	b.mu.Lock()
	f(&b.prog)
	p := b.prog
	b.mu.Unlock()
	if b.progress != nil {
		b.progress(p)
	}
}

func (b *Backup) stopped(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.stop != nil && b.stop() {
		return errPaused
	}
	return nil
}

func (b *Backup) readQuota(ctx context.Context) (Quota, error) {
	about, err := b.srv.About.Get().Fields("storageQuota(limit,usage)").Context(ctx).Do()
	if err != nil {
		return Quota{}, fmt.Errorf("read Drive storage: %w", err)
	}
	q := Quota{Used: about.StorageQuota.Usage, Limit: about.StorageQuota.Limit}
	if b.quota != nil {
		b.quota(q)
	}
	return q, nil
}

func jobCost(j uploadJob) int64 {
	if j.r == nil {
		return j.lf.Size
	}
	return max(j.lf.Size-j.r.Size, 0)
}

func fitJobs(jobs []uploadJob, q Quota, pct int) (fit, skipped []uploadJob, skippedBytes int64) {
	if q.Limit <= 0 {
		return jobs, nil, 0
	}
	allowed := q.Limit*int64(pct)/100 - q.Used
	for _, j := range jobs {
		c := jobCost(j)
		if c <= allowed {
			allowed -= c
			fit = append(fit, j)
			continue
		}
		skipped = append(skipped, j)
		skippedBytes += j.lf.Size
	}
	return fit, skipped, skippedBytes
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (b *Backup) Run(ctx context.Context) error {
	if len(b.folders) == 0 {
		_, err := b.readQuota(ctx)
		return err
	}
	b.report(func(p *Progress) { *p = Progress{Phase: "checking"} })
	remote, err := b.index.remoteOrList(ctx, b.srv)
	if err != nil {
		return fmt.Errorf("list drive: %w", err)
	}
	b.remote = remote
	target := backupTarget()
	names := backupNames(b.folders)
	seen := map[string]bool{}
	var errs []error
	var jobs []uploadJob
	var dirs []string
	complete := true

	sizes := map[string]FolderSize{}
	for i, dir := range b.folders {
		files, subdirs, scanErrs := scanTree(dir, b.exclude)
		errs = append(errs, scanErrs...)
		if files == nil {
			complete = false
			continue
		}
		sizes[dir] = measureFiles(files)
		prefix := path.Join(target, names[i])
		for _, d := range emptyDirs(files, subdirs) {
			dirs = append(dirs, path.Join(prefix, d))
		}
		keys := make([]string, 0, len(files))
		for rel := range files {
			keys = append(keys, rel)
		}
		sort.Strings(keys)
		for _, rel := range keys {
			if err := b.stopped(ctx); err != nil {
				return err
			}
			rp := path.Join(prefix, rel)
			seen[rp] = true
			if remote.blocked(rp) {
				continue
			}
			lf := files[rel]
			full := filepath.Join(dir, filepath.FromSlash(rel))
			b.report(func(p *Progress) { p.FilesTotal++; p.BytesTotal += lf.Size })
			job, err := b.plan(full, rp, lf)
			if err != nil {
				errs = append(errs, fmt.Errorf("check %s: %w", rp, err))
				continue
			}
			if job == nil {
				b.report(func(p *Progress) { p.FilesDone++; p.BytesDone += lf.Size })
				continue
			}
			jobs = append(jobs, *job)
		}
	}
	if b.sizes != nil {
		b.sizes(sizes)
	}
	q, err := b.readQuota(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	pct := b.limitPct
	if pct <= 0 || pct > 100 {
		pct = 100
	}
	jobs, skipped, skippedBytes := fitJobs(jobs, q, pct)
	if len(skipped) > 0 {
		complete = false
		errs = append(errs, fmt.Errorf("not enough Google Drive storage: %d file(s) (%s) not backed up, limit is %d%% of %s",
			len(skipped), humanBytes(skippedBytes), pct, humanBytes(q.Limit)))
		b.report(func(p *Progress) {
			p.FilesTotal -= len(skipped)
			p.BytesTotal -= skippedBytes
		})
	}

	b.report(func(p *Progress) { p.Phase = "uploading" })
	errs = append(errs, b.uploadAll(ctx, jobs)...)
	if err := b.stopped(ctx); err != nil {
		return err
	}
	for _, d := range dirs {
		if err := b.stopped(ctx); err != nil {
			return err
		}
		if remote.blocked(d) {
			continue
		}
		if _, err := b.folder(ctx, d); err != nil {
			errs = append(errs, fmt.Errorf("create folder %s: %w", d, err))
		}
	}
	if complete {
		for k := range b.st.Files {
			if !seen[k] {
				delete(b.st.Files, k)
			}
		}
	}
	b.readQuota(ctx)
	return errors.Join(errs...)
}

func (b *Backup) plan(full, rp string, lf LocalFile) (*uploadJob, error) {
	r, exists := b.remote.Files[rp]
	if !exists {
		return &uploadJob{full: full, rp: rp, lf: lf}, nil
	}
	sum := ""
	if e, ok := b.st.Files[rp]; ok && e.Size == lf.Size && e.MTime == lf.MTime {
		sum = e.MD5
	} else {
		var err error
		if sum, err = fileMD5(full); err != nil {
			return nil, err
		}
	}
	if r.MD5 == sum {
		b.st.Files[rp] = Entry{ID: r.ID, MD5: sum, Size: lf.Size, MTime: lf.MTime}
		return nil, nil
	}
	return &uploadJob{full: full, rp: rp, lf: lf, r: &r}, nil
}

func (b *Backup) uploadAll(ctx context.Context, jobs []uploadJob) []error {
	ch := make(chan uploadJob)
	var wg sync.WaitGroup
	var emu sync.Mutex
	var errs []error
	uploaded := 0
	for range uploadWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range ch {
				err := retry(ctx, func() error { return b.upload(ctx, job) })
				emu.Lock()
				if err != nil {
					errs = append(errs, fmt.Errorf("upload %s: %w", job.rp, err))
				} else if uploaded++; uploaded%saveEvery == 0 && b.save != nil {
					b.mu.Lock()
					if serr := b.save(); serr != nil {
						errs = append(errs, serr)
					}
					b.mu.Unlock()
				}
				emu.Unlock()
				b.report(func(p *Progress) {
					p.FilesDone++
					p.BytesDone += job.lf.Size
					if err == nil {
						p.Uploaded++
					}
				})
			}
		}()
	}
	for _, job := range jobs {
		if b.stopped(ctx) != nil {
			break
		}
		ch <- job
	}
	close(ch)
	wg.Wait()
	return errs
}

func (b *Backup) folder(ctx context.Context, dir string) (string, error) {
	b.folderMu.Lock()
	defer b.folderMu.Unlock()
	var id string
	err := retry(ctx, func() error {
		var err error
		id, err = b.remote.ensureFolder(ctx, b.srv, dir)
		return err
	})
	return id, err
}

func (b *Backup) upload(ctx context.Context, job uploadJob) error {
	if b.notify != nil {
		b.notify("upload " + job.rp)
	}
	meta := &drive.File{ModifiedTime: time.Unix(0, job.lf.MTime).UTC().Format(time.RFC3339Nano)}
	var res *drive.File
	var err error
	if b.resumer != nil && job.lf.Size >= resumableMin {
		t := uploadTarget{key: sessionKey(job.rp, job.lf), name: path.Base(job.rp), mtime: time.Unix(0, job.lf.MTime)}
		if job.r != nil {
			t.fileID = job.r.ID
		} else if t.parent, err = b.folder(ctx, path.Dir(job.rp)); err != nil {
			return err
		}
		var sent int64
		res, err = b.resumer.upload(ctx, t, job.full, job.lf.Size, b.limiter, func(offset int64) {
			b.report(func(p *Progress) { p.BytesDone += offset - sent })
			sent = offset
		})
		b.report(func(p *Progress) { p.BytesDone -= sent })
	} else if job.r != nil {
		f, ferr := os.Open(job.full)
		if ferr != nil {
			return ferr
		}
		defer f.Close()
		res, err = b.srv.Files.Update(job.r.ID, meta).Media(limitReader(ctx, f, b.limiter), googleapi.ChunkSize(uploadChunk)).Fields(fileFields).Context(ctx).Do()
	} else {
		parent, perr := b.folder(ctx, path.Dir(job.rp))
		if perr != nil {
			return perr
		}
		f, ferr := os.Open(job.full)
		if ferr != nil {
			return ferr
		}
		defer f.Close()
		meta.Name = path.Base(job.rp)
		meta.Parents = []string{parent}
		res, err = b.srv.Files.Create(meta).Media(limitReader(ctx, f, b.limiter), googleapi.ChunkSize(uploadChunk)).Fields(fileFields).Context(ctx).Do()
	}
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.st.Files[job.rp] = Entry{ID: res.Id, MD5: res.Md5Checksum, Size: job.lf.Size, MTime: job.lf.MTime}
	b.mu.Unlock()
	return nil
}
