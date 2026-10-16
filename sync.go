package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
)

type Syncer struct {
	srv         *drive.Service
	paths       Paths
	st          *State
	remote      *Remote
	force       bool
	dups        dupTracker
	notify      func(string)
	stop        func() bool
	progress    func(Progress)
	pending     func([]string)
	more        func()
	inbox       bool
	mu          sync.Mutex
	index       *driveIndex
	exclude     *excluder
	limiter     *limiter
	resumer     *resumer
	cache       *localCache
	excludeList []string
	planTook    time.Duration
	allow       func(int) bool
	dirty       bool
	journal     *idJournal
	lastLocal   map[string]LocalFile
}

func (s *Syncer) save() error {
	if !s.dirty {
		return nil
	}
	if err := s.st.save(s.paths.state()); err != nil {
		return err
	}
	s.dirty = false
	return s.journal.prune(func(key string) bool {
		kind, p, _ := strings.Cut(key, ":")
		if kind == "folder" {
			return s.remote != nil && s.remote.Folders[p] != ""
		}
		if _, ok := s.st.Files[p]; ok {
			return true
		}
		_, here := s.lastLocal[p]
		return s.lastLocal != nil && !here
	})
}

func (s *Syncer) abs(p string) string { return filepath.Join(s.paths.Root, filepath.FromSlash(p)) }

func (s *Syncer) Run(ctx context.Context) error {
	pruneTrash(s.paths.trash(), time.Now())
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
	remote.journal, remote.index = s.journal, s.index
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
	remote.journal, remote.index = s.journal, s.index
	s.remote = remote
	if len(remote.Files) == 0 && len(s.st.Files) > 0 && !s.force {
		return errors.New("drive listing is empty but state is not; refusing (use -force)")
	}
	dupErrs := s.renameDuplicates(ctx, remote)
	for p := range s.st.Unavailable {
		if _, ok := remote.Files[p]; !ok {
			delete(s.st.Unavailable, p)
			s.dirty = true
		}
	}
	var inboxErrs []error
	if s.inbox {
		inboxErrs = s.sortInbox(ctx, remote)
	}
	planStart := time.Now()
	local, unreadable, err := s.cache.scan(s.paths.Root, s.exclude, s.excludeList, planStart)
	if err != nil {
		return fmt.Errorf("scan local: %w", err)
	}
	s.lastLocal = local

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
		if !remote.blocked(p) && !underAny(unreadable, p) && !s.exclude.skip(s.abs(p), p) {
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
		if a != Noop {
			s.dirty = true
		}
		if a == DeleteLocal || a == DeleteRemote {
			deletes++
		}
	}
	errs := append(inboxErrs, dupErrs...)
	errs = append(errs, remote.invalidNameErrors()...)
	for _, u := range unreadable {
		errs = append(errs, fmt.Errorf("folder %q cannot be read on this computer; it is skipped until its permissions are fixed", u))
	}
	if massDelete(deletes, len(s.st.Files)) && !s.force && (s.allow == nil || !s.allow(deletes)) {
		for p, a := range plan {
			if a == DeleteLocal || a == DeleteRemote {
				plan[p] = Noop
			}
		}
		errs = append(errs, &massDeleteError{deletes: deletes, known: len(s.st.Files)})
	}
	var downloads []pendingDownload
	var uploads []pendingUpload
	var compares []pendingCompare
	var remoteDeletes []string
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
		if a == Download && remote.Files[p].MD5 != "" && s.st.Unavailable[p] == remote.Files[p].MD5 {
			continue
		}
		if a == Download {
			if info, err := os.Stat(s.abs(p)); err == nil && info.IsDir() {
				errs = append(errs, fmt.Errorf("download %s: a folder with the same name exists on this computer; rename one of them", p))
				continue
			}
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
		if a == Download || a == Compare || a == DeleteLocal || a == DeleteRemote {
			bm, lm, rm := "", "", ""
			if v, ok := s.st.Files[p]; ok {
				bm = v.MD5
			}
			if v, ok := local[p]; ok {
				lm = fmt.Sprintf("%d@%d", v.Size, v.MTime)
			}
			if v, ok := remote.Files[p]; ok {
				rm = v.MD5
			}
			log.Printf("plan %s %s: known=%s local=%s drive=%s", a, p, bm, lm, rm)
		}
		lf := local[p]
		rf := remote.Files[p]
		if a == Upload && s.waitForQuiet(p, lf, time.Now()) {
			continue
		}
		if a != Forget && a != Upload && a != Download {
			s.report(a.String() + " " + p)
		}
		if a == Download {
			_, here := local[p]
			downloads = append(downloads, pendingDownload{p, rf, scanView{lf, here}})
			continue
		}
		if a == Upload {
			uploads = append(uploads, pendingUpload{p, rf})
			continue
		}
		if a == Compare {
			compares = append(compares, pendingCompare{p, lf, rf})
			continue
		}
		if a == DeleteRemote {
			remoteDeletes = append(remoteDeletes, p)
			continue
		}
		if err := s.apply(ctx, a, p, lf, rf); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", a, p, err))
		}
	}
	s.planTook = time.Since(planStart)
	derrs := s.deleteRemoteAll(ctx, remoteDeletes)
	if slices.ContainsFunc(derrs, func(e error) bool { return errors.Is(e, errPaused) }) {
		return errPaused
	}
	errs = append(errs, derrs...)
	errs = append(errs, s.trashEmptyFolders(ctx, remote, remoteDeletes)...)
	conflicts, cerrs := s.compareAll(ctx, compares)
	errs = append(errs, cerrs...)
	for _, c := range conflicts {
		if err := s.apply(ctx, Compare, c.path, c.local, c.remote); err != nil {
			errs = append(errs, fmt.Errorf("compare %s: %w", c.path, err))
		}
	}
	uploads = s.prioritize(uploads, local)
	errs = append(errs, s.uploadAll(ctx, uploads)...)
	errs = append(errs, s.downloadAll(ctx, downloads)...)
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
		return s.download(ctx, p, r, nil)
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
		return s.download(ctx, p, r, &scanView{})
	}
	return nil
}

func (s *Syncer) report(line string) {
	if s.notify != nil {
		s.notify(line)
		return
	}
	log.Print(line)
}

func (s *Syncer) deleteLocal(p string) error {
	dst := filepath.Join(s.paths.trash(), time.Now().Format("20060102-150405"), filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := moveFile(s.abs(p), dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	delete(s.st.Files, p)
	return nil
}

func underAny(dirs []string, p string) bool {
	return slices.ContainsFunc(dirs, func(d string) bool { return p == d || strings.HasPrefix(p, d+"/") })
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
	for k := range r.Dupes {
		if strings.HasPrefix(k, foldKey(prefix)) {
			delete(r.Dupes, k)
		}
	}
	r.Ambiguous[foldKey(backupRootName)] = true
}
