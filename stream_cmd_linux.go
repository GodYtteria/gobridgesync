//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const streamRefresh = time.Minute

var streamUnmountWait = 8 * time.Second

func defaultStreamDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "GoogleDrive-Stream")
}

func mountFSType(mountinfo, dir string) (string, bool) {
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		sep := slices.Index(fields, "-")
		if sep < 5 || sep+1 >= len(fields) {
			continue
		}
		if strings.ReplaceAll(fields[4], "\\040", " ") == dir {
			return fields[sep+1], true
		}
	}
	return "", false
}

func cleanStaleMount(dir string) error {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("read mount table: %w", err)
	}
	return releaseStaleMount(string(b), dir)
}

func releaseStaleMount(mountinfo, dir string) error {
	if fstype, mounted := mountFSType(mountinfo, dir); mounted {
		if fstype != "fuse.gobridgesync" {
			return fmt.Errorf("%s is already used by another mount (%s); choose another folder", dir, fstype)
		}
		return lazyUnmount(dir)
	}
	if _, err := os.Stat(dir); errors.Is(err, syscall.ENOTCONN) {
		return lazyUnmount(dir)
	}
	return nil
}

var (
	fusermountTools = []string{"fusermount3", "fusermount"}
	runFusermount   = func(tool, dir string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, tool, "-uz", dir).CombinedOutput()
	}
)

func lazyUnmount(dir string) error {
	var errs []error
	for _, tool := range fusermountTools {
		out, err := runFusermount(tool, dir)
		if err == nil {
			log.Printf("streaming: released the mount at %s", dir)
			return nil
		}
		log.Printf("streaming: %s -uz %s: %v %s", tool, dir, err, strings.TrimSpace(string(out)))
		errs = append(errs, fmt.Errorf("%s: %w", tool, err))
	}
	return fmt.Errorf("could not release the stale mount at %s: %w", dir, errors.Join(errs...))
}

func unmountStream(unmount func() error, dir string) {
	if err := unmount(); err != nil {
		log.Printf("streaming: unmount %s: %v (a file is still open; releasing it lazily)", dir, err)
		if err := lazyUnmount(dir); err != nil {
			log.Printf("streaming: %v", err)
		}
	}
}

func releaseMount(unmount func() error, dir string) {
	unmounted := make(chan struct{})
	go func() {
		unmountStream(unmount, dir)
		close(unmounted)
	}()
	select {
	case <-unmounted:
	case <-time.After(streamUnmountWait):
		log.Printf("streaming: %s is still in use; waiting for it to be released", dir)
		<-unmounted
	}
}

func runStream(ctx context.Context, paths Paths, dir string, limitGB int, ready func()) error {
	if limitGB < 1 {
		return fmt.Errorf("-cache-gb must be at least 1")
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("streaming folder %q is not an absolute path (is the home folder set?)", dir)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lf, err := lock(filepath.Join(paths.StateDir, "stream"))
	if err != nil {
		return errors.New("streaming is already running")
	}
	defer lf.Close()
	srv, err := newService(ctx, paths)
	if err != nil {
		return err
	}
	if err := cleanStaleMount(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cache, err := newStreamCache(filepath.Join(paths.StateDir, "stream-cache"), int64(limitGB)<<30)
	if err != nil {
		return err
	}
	defer cache.Close()
	index := loadIndex(filepath.Join(paths.StateDir, "stream-index.json"))
	var snap atomic.Pointer[Remote]
	var hydrating atomic.Bool
	limit := int64(limitGB) << 30
	applyPins := func(r *Remote) {
		pins, err := (pinStore{file: paths.pins()}).load()
		if err != nil {
			log.Printf("pins: %v", err)
			return
		}
		files := pinnedFiles(r, pins)
		room := pinRoom(limit, diskBudget(paths.StateDir), cache.Used())
		if total := pinTotal(files); total > room/2 {
			log.Printf("pinned files are %s, more than half of the usable cache (%s); not pinning (raise -cache-gb, free disk space, or unpin something)", humanBytes(total), humanBytes(room))
			cache.setPinnedSet(map[string]string{})
			return
		}
		ids := map[string]string{}
		for _, f := range files {
			ids[f.ID] = f.MD5
		}
		cache.setPinnedSet(ids)
		if len(files) > 0 && hydrating.CompareAndSwap(false, true) {
			go func() {
				defer hydrating.Store(false)
				for p, f := range files {
					if ctx.Err() != nil {
						return
					}
					if !cache.isPinned(f.ID, f.MD5) {
						continue
					}
					if err := cache.Hydrate(ctx, f.ID, f.MD5, f.Size, driveRangeFetcher(srv, f.ID)); err != nil {
						log.Printf("keeping %s on this computer: %v", p, err)
					}
				}
			}()
		}
	}
	refresh := func() error {
		r, err := index.remote(ctx, srv)
		if err != nil {
			return err
		}
		snap.Store(r)
		applyPins(r)
		return nil
	}
	log.Printf("listing Google Drive (the first time can take a few minutes)")
	if err := refresh(); err != nil {
		return fmt.Errorf("list drive: %w", err)
	}
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	journal := loadIDJournal(filepath.Join(paths.StateDir, "stream-ids"))
	writer, err := newStreamWriter(srv, filepath.Join(paths.StateDir, "stream-stage"), cache, snap.Load, index,
		journal, func() {
			if r, err := index.snapshot(); err == nil {
				snap.Store(r.clone())
			}
		})
	if err != nil {
		return fmt.Errorf("prepare saving to Google Drive: %w", err)
	}
	writer.Start(ctx, uploadWorkers)
	server, err := mountStream(dir, &streamMount{snapshot: snap.Load, srv: srv, cache: cache, writer: writer, uid: uid, gid: gid})
	if err != nil {
		return fmt.Errorf("mount %s (is fuse3 installed?): %w", dir, err)
	}
	log.Printf("Google Drive is available at %s; press Ctrl+C to unmount", dir)
	if ready != nil {
		ready()
	}
	go func() {
		t := time.NewTicker(streamRefresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := refresh(); err != nil {
					log.Printf("refresh: %v", err)
				}
			}
		}
	}()
	waited := make(chan struct{})
	go func() {
		server.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		releaseMount(server.Unmount, dir)
	}
	return nil
}
