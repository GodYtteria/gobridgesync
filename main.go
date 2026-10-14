package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"
)

const usage = `usage: gobridgesync <command> [flags]

commands:
  login   authorize Google Drive access (backup files only; -full for whole Drive)
  sync    run one sync pass
  run     sync continuously
  ui      sync continuously with a web interface on localhost
  uninstall  remove GoBridgeSync (-purge also removes sign-in and settings)

flags:
  -root DIR        local sync folder (default ~/GoogleDrive)
  -interval DUR    pause between passes for run (default 1m)
  -force           allow mass deletions and empty-drive passes
  -addr ADDR       web interface address for ui (default 127.0.0.1:47811)
  -no-browser      do not open the browser for ui
  -tray            show a system tray icon for ui
  -full            login: grant whole-Drive access, needed for sync, run, and two-way sync`

func main() {
	debug.SetGCPercent(50)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	paths, err := defaultPaths()
	if err != nil {
		log.Fatal(err)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, usage) }
	root := fs.String("root", paths.Root, "")
	interval := fs.Duration("interval", time.Minute, "")
	force := fs.Bool("force", false, "")
	addr := fs.String("addr", defaultAddr(), "")
	noBrowser := fs.Bool("no-browser", false, "")
	tray := fs.Bool("tray", false, "")
	full := fs.Bool("full", false, "")
	purge := fs.Bool("purge", false, "")
	fs.Parse(os.Args[2:])
	if paths.Root, err = filepath.Abs(*root); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "login":
		err = login(ctx, paths, *full, "")
	case "sync":
		err = requireFull(paths, syncLoop(ctx, paths, *force, 0))
	case "run":
		if *interval < 10*time.Second {
			log.Fatal("interval must be at least 10s")
		}
		err = requireFull(paths, syncLoop(ctx, paths, *force, *interval))
	case "uninstall":
		err = uninstall(paths, *purge)
	case "ui":
		err = serveUI(ctx, paths, *force, *addr, !*noBrowser, *tray)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if restartRequested.Load() {
		if err := relaunch(); err != nil {
			log.Fatalf("restart after update: %v", err)
		}
		return
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func newSyncer(ctx context.Context, paths Paths, force bool) (*Syncer, error) {
	srv, err := newService(ctx, paths)
	if err != nil {
		return nil, err
	}
	st, err := loadState(paths.state())
	if err != nil {
		return nil, err
	}
	return &Syncer{srv: srv, paths: paths, st: st, force: force}, nil
}

func syncLoop(ctx context.Context, paths Paths, force bool, interval time.Duration) error {
	lf, err := lock(paths.StateDir)
	if err != nil {
		return err
	}
	defer lf.Close()
	s, err := newSyncer(ctx, paths, force)
	if err != nil {
		return err
	}
	for {
		err := errors.Join(s.Run(ctx), s.save())
		if interval == 0 {
			return err
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("sync: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func requireFull(paths Paths, err error) error {
	if err != nil && internalEdition() && !fullAccess(paths) {
		return fmt.Errorf("%w (two-way sync needs whole-Drive access: run gobridgesync login -full)", err)
	}
	return err
}
