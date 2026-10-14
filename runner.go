package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/api/drive/v3"
)

const recentLimit = 50

type Status struct {
	State          string                `json:"state"`
	Message        string                `json:"message"`
	LastSync       time.Time             `json:"lastSync"`
	LastError      string                `json:"lastError"`
	Recent         []string              `json:"recent"`
	Paused         bool                  `json:"paused"`
	HasCredentials bool                  `json:"hasCredentials"`
	LoggedIn       bool                  `json:"loggedIn"`
	Root           string                `json:"root"`
	Home           string                `json:"home"`
	Folders        []string              `json:"folders"`
	Exclude        []string              `json:"exclude"`
	Suggestions    []string              `json:"suggestions"`
	FolderSizes    map[string]FolderSize `json:"folderSizes"`
	TwoWay         bool                  `json:"twoWay"`
	StorageLimit   int                   `json:"storageLimit"`
	SpeedLimitKB   int                   `json:"speedLimitKB"`
	Every          int                   `json:"intervalMinutes"`
	StartAtLogin   bool                  `json:"startAtLogin"`
	AutoUpdate     bool                  `json:"autoUpdate"`
	Version        string                `json:"version"`
	AppName        string                `json:"appName"`
	OS             string                `json:"os"`
	Update         string                `json:"updateAvailable"`
	Updating       bool                  `json:"updating"`
	LoginURL       string                `json:"loginUrl"`
	Target         string                `json:"target"`
	Progress       Progress              `json:"progress"`
	Quota          *Quota                `json:"quota"`
	LastRun        *RunStats             `json:"lastRun"`
	Issues         []Issue               `json:"issues"`
}

type RunStats struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Seconds  float64   `json:"seconds"`
	Uploaded int       `json:"uploaded"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	Errors   int       `json:"errors"`
}

type Issue struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

const issueLimit = 100

func flatten(err error) []string {
	if err == nil {
		return nil
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []string
		for _, e := range j.Unwrap() {
			out = append(out, flatten(e)...)
		}
		return out
	}
	return []string{err.Error()}
}

func (r *Runner) alert(issues []string, q *Quota, now time.Time) {
	var send [][2]string
	r.mu.Lock()
	if len(issues) > 0 {
		msg := issueSummary(issues)
		if shouldAlert(r.alertMsg, r.alertAt, now, msg) {
			r.alertMsg, r.alertAt = msg, now
			send = append(send, [2]string{"GoBridgeSync: backup problem", msg})
		}
		r.alerting = true
	} else if r.alerting {
		r.alerting, r.alertMsg = false, ""
		send = append(send, [2]string{"GoBridgeSync", "Backup is working again."})
	}
	if w := quotaWarning(q); w != "" && now.Sub(r.quotaAt) >= quotaAlertRepeat {
		r.quotaAt = now
		send = append(send, [2]string{"GoBridgeSync: storage almost full", w})
	}
	notify := r.notify
	r.mu.Unlock()
	for _, n := range send {
		if err := notify(n[0], n[1]); err != nil {
			log.Printf("notify: %v", err)
		}
	}
}

func mergeIssues(existing []Issue, msgs []string, now time.Time) []Issue {
	out := make([]Issue, 0, len(existing)+len(msgs))
	fresh := map[string]bool{}
	for _, m := range msgs {
		if !fresh[m] {
			fresh[m] = true
			out = append(out, Issue{Time: now, Message: m})
		}
	}
	for _, i := range existing {
		if !fresh[i.Message] {
			out = append(out, i)
		}
	}
	return out
}

func (r *Runner) ClearIssues() {
	r.set(func(s *Status) { s.Issues = nil })
	r.saveHistory()
}

type Runner struct {
	paths    Paths
	force    bool
	trigger  chan struct{}
	updater  *updater
	restart  func()
	jobs     chan restoreJob
	signout  chan chan error
	snapshot atomic.Pointer[Remote]
	srv      atomic.Pointer[drive.Service]

	mu        sync.Mutex
	status    Status
	loggingIn bool
	loggedIn  bool

	alertMsg string
	alertAt  time.Time
	quotaAt  time.Time
	alerting bool
	notify   func(title, body string) error
}

func newRunner(paths Paths, force bool) *Runner {
	cfg, err := editConfig(paths, func(*AppConfig) error { return nil })
	if err != nil {
		log.Printf("read settings: %v", err)
	}
	h := loadHistory(paths)
	return &Runner{
		paths:   paths,
		force:   force,
		trigger: make(chan struct{}, 1),
		status:  Status{State: "starting", Recent: h.Recent, Issues: h.Issues, LastRun: h.LastRun, Paused: cfg.Paused},
		notify:  desktopNotify,
		updater: newUpdater(),
		restart: func() {},
		jobs:    make(chan restoreJob, 1),
		signout: make(chan chan error),
	}
}

func (r *Runner) SignOut(ctx context.Context) error {
	done := make(chan error, 1)
	select {
	case r.signout <- done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return <-done
}

func (r *Runner) signOutNow(ps *passState) error {
	*ps = passState{}
	r.snapshot.Store(nil)
	r.srv.Store(nil)
	err := errors.Join(deleteToken(r.paths), resetAccountState(r.paths))
	r.mu.Lock()
	r.loggedIn = false
	r.mu.Unlock()
	r.set(func(s *Status) {
		s.Quota, s.Update, s.State, s.Message = nil, "", "signin", "Sign in with Google to begin"
	})
	r.addRecent("signed out")
	r.Trigger()
	return err
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return !errors.Is(err, fs.ErrNotExist)
}

func (r *Runner) Snapshot() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.status
	st.Recent = append(make([]string, 0, len(r.status.Recent)), r.status.Recent...)
	st.Issues = append(make([]Issue, 0, len(r.status.Issues)), r.status.Issues...)
	st.HasCredentials = hasClient(r.paths)
	st.LoggedIn = r.loggedIn
	st.Root = r.paths.Root
	st.Version = version
	st.AppName = releaseName()
	st.OS = runtime.GOOS
	st.Home, _ = os.UserHomeDir()
	st.Target = "My Drive/" + backupTarget()
	if cfg, err := loadAppConfig(r.paths); err == nil {
		st.Suggestions = folderSuggestions(st.Home, cfg.Folders, r.paths.Root)
		st.Folders, st.TwoWay, st.StorageLimit, st.Exclude = cfg.Folders, cfg.TwoWay, cfg.limit(), cfg.Exclude
		st.Every, st.StartAtLogin, st.AutoUpdate, st.SpeedLimitKB = cfg.every(), cfg.startAtLogin(), cfg.autoUpdate(), cfg.speed()
	}
	if r.loggingIn {
		st.State = "login"
		st.Message = "Waiting for Google sign-in in your browser"
	}
	return st
}

func (r *Runner) set(f func(*Status)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(&r.status)
}

func (r *Runner) addRecent(line string) {
	r.set(func(s *Status) {
		s.Recent = append([]string{time.Now().Format("15:04:05") + " " + line}, s.Recent...)
		if len(s.Recent) > recentLimit {
			s.Recent = s.Recent[:recentLimit]
		}
	})
}

func (r *Runner) Trigger() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

func (r *Runner) UpdateConfig(f func(*AppConfig)) error {
	cfg, err := editConfig(r.paths, func(c *AppConfig) error {
		f(c)
		return nil
	})
	if err != nil {
		return err
	}
	r.set(func(s *Status) { s.Paused = cfg.Paused })
	r.Trigger()
	return nil
}

func (r *Runner) SetPaused(p bool) error {
	return r.UpdateConfig(func(c *AppConfig) { c.Paused = p })
}

func (r *Runner) Login(ctx context.Context, returnURL string) error {
	r.mu.Lock()
	if r.loggingIn {
		r.mu.Unlock()
		return errors.New("sign-in already in progress")
	}
	r.loggingIn = true
	r.mu.Unlock()
	go func() {
		err := login(ctx, r.paths, false, returnURL, func(u string) {
			r.set(func(s *Status) { s.LoginURL = u })
		})
		r.mu.Lock()
		r.loggingIn = false
		r.status.LoginURL = ""
		r.mu.Unlock()
		if err != nil {
			r.set(func(s *Status) { s.LastError = "sign-in: " + err.Error() })
			return
		}
		r.addRecent("signed in")
		r.Trigger()
	}()
	return nil
}

func (r *Runner) isPaused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status.Paused
}

type passState struct {
	srv     *drive.Service
	resumer *resumer
	backup  *State
	sync    *Syncer
	index   *driveIndex
}

func (r *Runner) pass(ctx context.Context, ps *passState) error {
	if ps.srv == nil {
		srv, client, err := connect(ctx, r.paths)
		if err != nil {
			return fmt.Errorf("connect to Google Drive: %w", err)
		}
		ps.srv = srv
		ps.resumer = &resumer{client: client, sessions: loadSessions(r.paths.sessions()), endpoint: uploadEndpoint}
		ps.index = loadIndex(r.paths.index())
		r.srv.Store(srv)
	}
	cfg, err := loadAppConfig(r.paths)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}
	if ps.backup == nil {
		if ps.backup, err = loadState(r.paths.backupState()); err != nil {
			return err
		}
	}
	saveBackup := func() error { return ps.backup.save(r.paths.backupState()) }
	r.set(func(st *Status) { st.State, st.Message = "syncing", "Backing up" })
	b := &Backup{
		srv: ps.srv, folders: cfg.Folders, st: ps.backup, save: saveBackup, stop: r.isPaused, limitPct: cfg.limit(), index: ps.index,
		exclude:  newExcluder(cfg.Exclude),
		notify:   r.addRecent,
		progress: func(p Progress) { r.set(func(st *Status) { st.Progress = p }) },
		quota:    func(q Quota) { r.set(func(st *Status) { st.Quota = &q }) },
		sizes:    func(m map[string]FolderSize) { r.set(func(st *Status) { st.FolderSizes = m }) },
	}
	err = errors.Join(b.Run(ctx), saveBackup())
	if errors.Is(err, errPaused) || !cfg.TwoWay {
		return err
	}
	if internalEdition() && !fullAccess(r.paths) {
		return errors.Join(err, errors.New("whole-Drive sync needs full access: sign out and sign in again"))
	}
	if ps.sync == nil {
		st, lerr := loadState(r.paths.state())
		if lerr != nil {
			return errors.Join(err, lerr)
		}
		ps.sync = &Syncer{srv: ps.srv, paths: r.paths, st: st, force: r.force, notify: r.addRecent, stop: r.isPaused, index: ps.index}
	}
	ps.sync.exclude, ps.sync.limiter, ps.sync.resumer = newExcluder(cfg.Exclude), cfg.limiter(), ps.resumer
	r.set(func(st *Status) { st.State, st.Message = "syncing", "Syncing "+filepath.Base(r.paths.Root) })
	return errors.Join(err, ps.sync.Run(ctx), ps.sync.save())
}

func (r *Runner) checkLogin() bool {
	ok := hasToken(r.paths)
	r.mu.Lock()
	r.loggedIn = ok
	r.mu.Unlock()
	return ok
}

func (r *Runner) Loop(ctx context.Context) {
	ps := &passState{}
	for {
		switch {
		case r.isPaused():
			r.set(func(st *Status) { st.State, st.Message = "paused", "Paused" })
		case !hasClient(r.paths):
			*ps = passState{}
			r.set(func(st *Status) { st.State, st.Message = "setup", "Add your Google OAuth client file to begin" })
		case !r.checkLogin():
			*ps = passState{}
			r.set(func(st *Status) { st.State, st.Message = "signin", "Sign in with Google to begin" })
		default:
			started := time.Now()
			err := r.pass(ctx, ps)
			r.refreshSnapshot(ps)
			debug.FreeOSMemory()
			if ctx.Err() != nil {
				return
			}
			issues := flatten(err)
			if errors.Is(err, errPaused) {
				issues = nil
			} else {
				r.alert(issues, r.Snapshot().Quota, time.Now())
			}
			r.set(func(st *Status) {
				now := time.Now()
				st.LastRun = &RunStats{
					Started: started, Finished: now, Seconds: now.Sub(started).Seconds(),
					Uploaded: st.Progress.Uploaded, Files: st.Progress.FilesTotal, Bytes: st.Progress.BytesTotal, Errors: len(issues),
				}
				st.Issues = mergeIssues(st.Issues, issues, now)
				if len(st.Issues) > issueLimit {
					st.Issues = st.Issues[:issueLimit]
				}
				switch {
				case errors.Is(err, errPaused):
					st.State, st.Message = "paused", "Paused"
				case err != nil:
					log.Printf("sync: %v", err)
					st.LastSync = time.Now()
					st.State, st.Message, st.LastError = "error", fmt.Sprintf("Last backup had %d problem(s)", len(issues)), ""
				default:
					st.LastSync = time.Now()
					st.State, st.Message, st.LastError = "idle", "Up to date", ""
				}
			})
		}
		r.saveHistory()
		var wait <-chan time.Time
		cfg, err := loadAppConfig(r.paths)
		if err != nil {
			cfg = AppConfig{}
		}
		if d, ok := passInterval(cfg.every()); ok {
			wait = time.After(d)
		}
		select {
		case <-ctx.Done():
			return
		case <-r.trigger:
		case <-wait:
		case job := <-r.jobs:
			r.restoreNow(ctx, ps, job)
		case done := <-r.signout:
			done <- r.signOutNow(ps)
		}
	}
}

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

func (r *Runner) refreshSnapshot(ps *passState) {
	if ps.index == nil {
		return
	}
	rem, err := buildRemote(ps.index.Files, ps.index.RootID)
	if err == nil {
		r.snapshot.Store(rem)
	}
}

func (r *Runner) restoreNow(ctx context.Context, ps *passState, job restoreJob) {
	if ps.srv == nil {
		srv, err := newService(ctx, r.paths)
		if err != nil {
			r.recordIssues([]string{"restore: " + err.Error()})
			return
		}
		ps.srv = srv
		ps.index = loadIndex(r.paths.index())
		r.srv.Store(srv)
	}
	prev := r.Snapshot()
	err := r.runRestore(ctx, ps.srv, job)
	r.set(func(s *Status) { s.State, s.Message = prev.State, prev.Message })
	if err != nil {
		r.recordIssues(flatten(err))
	}
	r.saveHistory()
}

func (r *Runner) recordIssues(msgs []string) {
	r.set(func(s *Status) {
		s.Issues = mergeIssues(s.Issues, msgs, time.Now())
		if len(s.Issues) > issueLimit {
			s.Issues = s.Issues[:issueLimit]
		}
	})
}

const (
	instantRemoteCheck = time.Minute
	watchEvery         = 10 * time.Second
	watchEveryLarge    = 30 * time.Second
	largeTree          = 10000
)

func passInterval(every int) (time.Duration, bool) {
	switch {
	case every == manualSync:
		return 0, false
	case every == instantSync:
		return instantRemoteCheck, true
	case every > 0:
		return time.Duration(every) * time.Minute, true
	}
	return defaultEveryMinutes * time.Minute, true
}

func localFingerprint(cfg AppConfig, root string) (string, int) {
	ex := newExcluder(cfg.Exclude)
	h := sha256.New()
	count := 0
	add := func(prefix string, files map[string]LocalFile) {
		count += len(files)
		keys := make([]string, 0, len(files))
		for k := range files {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "%s/%s\x00%d\x00%d\n", prefix, k, files[k].Size, files[k].MTime)
		}
	}
	for _, dir := range cfg.Folders {
		files, _, _ := scanTree(dir, ex)
		add(dir, files)
	}
	if cfg.TwoWay {
		if files, err := scanLocal(root, ex); err == nil {
			add(root, files)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), count
}

func (r *Runner) WatchLoop(ctx context.Context) {
	last, every, seenPass := "", watchEvery, time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		cfg, err := loadAppConfig(r.paths)
		if err != nil || cfg.every() != instantSync || r.isPaused() {
			last = ""
			continue
		}
		fp, files := localFingerprint(cfg, r.paths.Root)
		every = watchEvery
		if files > largeTree {
			every = watchEveryLarge
		}
		passAt := r.Snapshot().LastSync
		if last != "" && fp != last && passAt.Equal(seenPass) {
			r.Trigger()
		}
		last, seenPass = fp, passAt
	}
}

type Measurement struct {
	FolderSize
	Free  int64 `json:"free"`
	Known bool  `json:"known"`
	Fits  bool  `json:"fits"`
}

func (r *Runner) Measure(raw string) (Measurement, error) {
	dir, err := expandHome(strings.TrimSpace(raw))
	if err != nil {
		return Measurement{}, err
	}
	if !filepath.IsAbs(dir) {
		return Measurement{}, errors.New("use a full path like ~/Documents")
	}
	cfg, _ := loadAppConfig(r.paths)
	if _, err := normalizeFolders(append(append([]string{}, cfg.Folders...), dir), r.paths.Root, cfg.Folders); err != nil {
		return Measurement{}, err
	}
	files, _, errs := scanTree(filepath.Clean(dir), newExcluder(cfg.Exclude))
	if files == nil {
		return Measurement{}, errors.Join(errs...)
	}
	m := Measurement{FolderSize: measureFiles(files), Fits: true}
	if q := r.Snapshot().Quota; q != nil && q.Limit > 0 {
		m.Known = true
		m.Free = max(q.Limit*int64(cfg.limit())/100-q.Used, 0)
		m.Fits = m.Bytes <= m.Free
	}
	return m, nil
}
