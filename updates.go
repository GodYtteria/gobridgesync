package main

import (
	"context"
	"errors"
	"log"
	"time"
)

const (
	updateFirstCheck = 2 * time.Minute
	updateEvery      = 24 * time.Hour
	updateRetry      = 10 * time.Minute
)

func (r *Runner) UpdateLoop(ctx context.Context) {
	wait := updateFirstCheck
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = updateEvery
		tag, err := r.updater.available(ctx)
		if err != nil {
			log.Printf("check for updates: %v", err)
			wait = updateRetry
			continue
		}
		r.set(func(s *Status) { s.Update = tag })
		if tag == "" {
			continue
		}
		cfg, err := loadAppConfig(r.paths)
		if err != nil || !cfg.autoUpdate() || !canReplaceExecutable() {
			continue
		}
		if r.Snapshot().State == "syncing" {
			wait = updateRetry
			continue
		}
		if err := r.ApplyUpdate(ctx); err != nil {
			log.Printf("update: %v", err)
			wait = updateRetry
		}
	}
}

func (r *Runner) ApplyUpdate(ctx context.Context) error {
	if !canReplaceExecutable() {
		return errors.New("GoBridgeSync was installed by your package manager; update it there")
	}
	r.mu.Lock()
	tag, busy := r.status.Update, r.status.Updating
	if tag == "" || busy {
		r.mu.Unlock()
		return errors.New("no update to install")
	}
	r.status.Updating = true
	r.mu.Unlock()
	err := r.installUpdate(ctx, tag)
	if err != nil {
		r.set(func(s *Status) { s.Updating = false })
		r.addRecent("update failed: " + err.Error())
		return err
	}
	r.addRecent("updated to " + tag + ", restarting")
	r.restart()
	return nil
}

func (r *Runner) installUpdate(ctx context.Context, tag string) error {
	bin, err := r.updater.download(ctx, tag)
	if err != nil {
		return err
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	return replaceExecutable(exe, bin)
}
