package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

const reportsWrites = runtime.GOOS != "darwin"

const (
	debounce      = 2 * time.Second
	debounceMax   = 30 * time.Second
	watchRecheck  = 30 * time.Second
	watchRetry    = 5 * time.Minute
	watchFallback = "file watching unavailable, checking for changes every few seconds instead"
)

func watchRoots(cfg AppConfig, syncRoot string) []string {
	roots := append([]string{}, cfg.Folders...)
	if cfg.TwoWay {
		roots = append(roots, syncRoot)
	}
	return roots
}

func addTree(w *fsnotify.Watcher, root string, ex *excluder) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != root {
			if rel, rerr := filepath.Rel(root, p); rerr == nil && ex.skip(p, filepath.ToSlash(rel)) {
				return filepath.SkipDir
			}
		}
		return w.Add(p)
	})
}

func ignoredEvent(name string, roots []string, ex *excluder) bool {
	base := filepath.Base(name)
	if strings.HasPrefix(base, tmpPrefix) {
		return true
	}
	for _, root := range roots {
		if within(name, root) {
			rel, err := filepath.Rel(root, name)
			return err == nil && rel != "." && ex.skip(name, filepath.ToSlash(rel))
		}
	}
	return true
}

func (r *Runner) WatchLoop(ctx context.Context) {
	for ctx.Err() == nil {
		cfg, err := loadAppConfig(r.paths)
		if err != nil || cfg.every() != instantSync || r.isPaused() {
			if !sleepCtx(ctx, watchEvery) {
				return
			}
			continue
		}
		if !r.watchEvents(ctx, cfg) {
			r.recordIssues([]string{"real-time file watching unavailable; checking for changes every few seconds instead"})
			r.pollUntilChanged(ctx, cfg)
		}
	}
}

func debounceDelay(first, now time.Time) time.Duration {
	return max(0, min(debounce, first.Add(debounceMax).Sub(now)))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (r *Runner) pollUntilChanged(ctx context.Context, cfg AppConfig) {
	last, every := "", watchEvery
	retryAt := time.Now().Add(watchRetry)
	for ctx.Err() == nil && time.Now().Before(retryAt) {
		cur, err := loadAppConfig(r.paths)
		if err != nil || !slices.Equal(watchRoots(cur, r.paths.Root), watchRoots(cfg, r.paths.Root)) || cur.every() != instantSync || r.isPaused() {
			return
		}
		r.pollChanges(ctx, cur, &last, &every)
		if !sleepCtx(ctx, every) {
			return
		}
	}
}

func (r *Runner) watchEvents(ctx context.Context, cfg AppConfig) bool {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("%s: %v", watchFallback, err)
		return false
	}
	defer w.Close()
	ex := newExcluder(cfg.Exclude)
	roots := watchRoots(cfg, r.paths.Root)
	for _, root := range roots {
		if err := addTree(w, root, ex); err != nil {
			log.Printf("%s: %v", watchFallback, err)
			return false
		}
	}
	r.local.setWatching(reportsWrites)
	defer r.local.setWatching(false)
	var fire <-chan time.Time
	var first time.Time
	degraded := false
	recheck := time.NewTicker(watchRecheck)
	defer recheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return true
		case <-fire:
			fire = nil
			r.Changed()
		case <-recheck.C:
			cur, err := loadAppConfig(r.paths)
			if err != nil || cur.every() != instantSync || r.isPaused() ||
				!slices.Equal(watchRoots(cur, r.paths.Root), roots) || !slices.Equal(cur.Exclude, cfg.Exclude) {
				return true
			}
		case ev, ok := <-w.Events:
			if !ok {
				return true
			}
			if ignoredEvent(ev.Name, roots, ex) {
				continue
			}
			r.local.changed(ev.Name)
			if ev.Has(fsnotify.Create) {
				if err := addTree(w, ev.Name, ex); err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
					log.Printf("watch %s: %v", ev.Name, err)
					r.recordIssues([]string{fmt.Sprintf("cannot watch %s for changes (%v); checking the whole folder each time instead", ev.Name, err)})
					degraded = true
					r.local.setWatching(false)
				}
			}
			now := time.Now()
			if fire == nil {
				first = now
			}
			fire = time.After(debounceDelay(first, now))
		case err, ok := <-w.Errors:
			if !ok {
				return true
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				log.Printf("watch: %v; checking everything", err)
				r.local.setWatching(reportsWrites && !degraded)
				r.Changed()
				continue
			}
			log.Printf("%s: %v", watchFallback, err)
			return false
		}
	}
}
