package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	AutoSortInbox  bool                  `json:"autoSortInbox"`
	SendReports    bool                  `json:"sendReports"`
	DevMode        bool                  `json:"developerMode"`
	Stream         bool                  `json:"streamFolder"`
	StreamGB       int                   `json:"streamGB"`
	StreamState    string                `json:"streamState"`
	StreamDir      string                `json:"streamDir"`
	AskReports     bool                  `json:"askReports"`
	ReportsReady   bool                  `json:"reportsAvailable"`
	Version        string                `json:"version"`
	AppName        string                `json:"appName"`
	OS             string                `json:"os"`
	Channel        string                `json:"channel"`
	Update         string                `json:"updateAvailable"`
	Updating       bool                  `json:"updating"`
	LoginURL       string                `json:"loginUrl"`
	Pending        []string              `json:"pending"`
	PendingCount   int                   `json:"pendingCount"`
	Target         string                `json:"target"`
	Progress       Progress              `json:"progress"`
	Quota          *Quota                `json:"quota"`
	PendingDeletes int                   `json:"pendingDeletes"`
	Notices        []string              `json:"notices"`
	LastRun        *RunStats             `json:"lastRun"`
	Issues         []Issue               `json:"issues"`
}

type RunStats struct {
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished"`
	Seconds    float64   `json:"seconds"`
	Uploaded   int       `json:"uploaded"`
	Downloaded int       `json:"downloaded"`
	Files      int       `json:"files"`
	Bytes      int64     `json:"bytes"`
	Errors     int       `json:"errors"`
}

const issueLimit = 100

type Runner struct {
	paths    Paths
	force    bool
	trigger  chan struct{}
	changed  chan struct{}
	allowed  int
	allowAt  time.Time
	local    *localCache
	pending  []string
	reports  *reporter
	updater  *updater
	restart  func()
	passStop context.CancelFunc
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
		changed: make(chan struct{}, 1),
		local:   newLocalCache(),
		status:  Status{State: "starting", Recent: h.Recent, Issues: h.Issues, LastRun: h.LastRun, Paused: cfg.Paused},
		notify:  desktopNotify,
		updater: newUpdater(),
		restart: func() {},
		reports: newReporter(paths.logFile()),
		jobs:    make(chan restoreJob, 1),
		signout: make(chan chan error),
	}
}

func (r *Runner) SignOut(ctx context.Context) error {
	done := make(chan error, 1)
	r.mu.Lock()
	stop := r.passStop
	r.mu.Unlock()
	if stop != nil {
		stop()
	}
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
	st.Channel = buildChannel()
	st.Home, _ = os.UserHomeDir()
	st.Target = "My Drive/" + backupTarget()
	if cfg, err := loadAppConfig(r.paths); err == nil {
		st.Suggestions = folderSuggestions(st.Home, cfg.Folders, r.paths.Root)
		st.Folders, st.TwoWay, st.StorageLimit, st.Exclude = cfg.Folders, cfg.TwoWay, cfg.limit(), cfg.Exclude
		st.Every, st.StartAtLogin, st.AutoUpdate, st.SpeedLimitKB = cfg.every(), cfg.startAtLogin(), cfg.autoUpdate(), cfg.speed()
		st.AutoSortInbox = cfg.inboxSort()
		st.DevMode = cfg.DevMode
		st.Stream, st.StreamGB, st.StreamDir = cfg.Stream, cfg.streamGB(), defaultStreamDir()
		st.ReportsReady = reportURL != ""
		st.SendReports, st.AskReports = st.ReportsReady && cfg.sendReports(), st.ReportsReady && cfg.reportsUndecided()
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
	log.Print(line)
	r.set(func(s *Status) {
		s.Recent = append([]string{time.Now().Format("15:04:05") + " " + line}, s.Recent...)
		if len(s.Recent) > recentLimit {
			s.Recent = s.Recent[:recentLimit]
		}
	})
}

func (r *Runner) Changed() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
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
	r.mu.Lock()
	r.status.Paused = cfg.Paused
	stop := r.passStop
	r.mu.Unlock()
	if cfg.Paused && stop != nil {
		stop()
	}
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
			log.Printf("sign-in failed: %v", err)
			r.set(func(s *Status) { s.LastError = "sign-in: " + err.Error() })
			return
		}
		r.mu.Lock()
		r.loggedIn = true
		r.mu.Unlock()
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
	ex      *excluder
	exList  []string

	backupIDs *idJournal
}

func (r *Runner) pass(ctx, connCtx context.Context, ps *passState) error {
	if ps.srv == nil {
		srv, client, err := connect(connCtx, r.paths)
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
	if ps.backupIDs == nil {
		ps.backupIDs = loadIDJournal(r.paths.backupIDs())
	}
	if ps.backup == nil {
		if ps.backup, err = loadState(r.paths.backupState()); err != nil {
			return err
		}
	}
	saveBackup := func() error { return ps.backup.save(r.paths.backupState()) }
	r.set(func(st *Status) { st.State, st.Message = "syncing", "Backing up" })
	b := &Backup{
		srv: ps.srv, folders: cfg.Folders, st: ps.backup, save: saveBackup, stop: r.isPaused, limitPct: cfg.limit(), index: ps.index, journal: ps.backupIDs, covered: coveredRoots(cfg, r.paths.Root),
		exclude:  newExcluder(cfg.Exclude),
		notify:   r.addRecent,
		progress: func(p Progress) { r.set(func(st *Status) { st.Progress = p }) },
		quota:    func(q Quota) { r.set(func(st *Status) { st.Quota = &q }) },
		sizes:    func(m map[string]FolderSize) { r.set(func(st *Status) { st.FolderSizes = m }) },
		pending:  r.setPending,
		more:     r.Trigger,
	}
	runErr := b.Run(ctx)
	r.set(func(st *Status) { st.Notices = b.notices })
	saveErr := saveBackup()
	if saveErr == nil {
		saveErr = ps.backupIDs.prune(func(key string) bool {
			kind, p, _ := strings.Cut(key, ":")
			if kind == "folder" {
				return b.remote != nil && b.remote.Folders[p] != ""
			}
			_, ok := ps.backup.Files[p]
			return ok
		})
	}
	err = errors.Join(runErr, saveErr)
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
		ps.sync = &Syncer{srv: ps.srv, paths: r.paths, st: st, force: r.force, notify: r.addRecent, stop: r.isPaused, index: ps.index,
			progress: func(p Progress) { r.set(func(s *Status) { s.Progress = p }) }, pending: r.setPending, more: r.Trigger, allow: r.allowDeletes,
			journal: loadIDJournal(r.paths.syncIDs())}
	}
	if ps.ex == nil || !slices.Equal(ps.exList, cfg.Exclude) {
		ps.ex, ps.exList = newExcluder(cfg.Exclude), slices.Clone(cfg.Exclude)
	}
	ps.sync.exclude, ps.sync.limiter, ps.sync.resumer = ps.ex, cfg.limiter(), ps.resumer
	ps.sync.inbox = cfg.inboxSort()
	ps.sync.cache, ps.sync.excludeList = r.local, cfg.Exclude
	r.set(func(st *Status) { st.State, st.Message = "syncing", "Syncing "+filepath.Base(r.paths.Root) })
	return errors.Join(err, ps.sync.Run(ctx), ps.sync.save())
}

func coveredRoots(cfg AppConfig, syncRoot string) []string {
	roots := append([]string{}, cfg.Folders...)
	if cfg.TwoWay {
		roots = append(roots, syncRoot)
	}
	return roots
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
	var lastStart time.Time
	var lastTook time.Duration
	for {
		switch {
		case !hasClient(r.paths):
			*ps = passState{}
			r.set(func(st *Status) { st.State, st.Message = "setup", "Add your Google OAuth client file to begin" })
		case !r.checkLogin():
			*ps = passState{}
			r.set(func(st *Status) { st.State, st.Message = "signin", "Sign in with Google to begin" })
		case r.isPaused():
			r.set(func(st *Status) { st.State, st.Message = "paused", "Paused" })
		default:
			started := time.Now()
			passCtx, cancel := context.WithCancel(ctx)
			r.mu.Lock()
			r.passStop = cancel
			r.mu.Unlock()
			err := r.pass(passCtx, ctx, ps)
			lastStart, lastTook = started, time.Since(started)
			r.mu.Lock()
			if started.After(r.allowAt) {
				r.allowed = 0
			}
			r.mu.Unlock()
			if ps.sync != nil && ps.sync.planTook > 0 {
				lastTook = ps.sync.planTook
			}
			stoppedEarly := passCtx.Err() != nil
			cancel()
			r.mu.Lock()
			r.passStop = nil
			r.mu.Unlock()
			if ctx.Err() == nil && stoppedEarly {
				err = errPaused
			}
			r.refreshSnapshot(ps)
			var mde *massDeleteError
			pending := 0
			if errors.As(err, &mde) {
				pending = mde.deletes
			}
			r.set(func(st *Status) {
				st.PendingDeletes = pending
				if pending == 0 {
					st.Issues = slices.DeleteFunc(st.Issues, func(i Issue) bool { return strings.HasSuffix(i.Message, massDeleteWaiting) })
				}
			})
			if ctx.Err() != nil {
				return
			}
			issues := flatten(err)
			if errors.Is(err, errPaused) {
				issues = nil
			} else {
				r.alert(issues, r.Snapshot().Quota, time.Now())
				if cfg, cerr := loadAppConfig(r.paths); cerr == nil && cfg.sendReports() && reportURL != "" && len(issues) > 0 {
					go func(problems []string) {
						if n := r.reports.send(ctx, problems, time.Now()); n > 0 {
							log.Printf("sent %d problem report(s)", n)
						}
					}(append([]string(nil), issues...))
				}
			}
			r.set(func(st *Status) {
				now := time.Now()
				st.LastRun = &RunStats{
					Started: started, Finished: now, Seconds: now.Sub(started).Seconds(),
					Uploaded: st.Progress.Uploaded, Downloaded: st.Progress.Downloaded, Files: st.Progress.FilesTotal, Bytes: st.Progress.BytesTotal, Errors: len(issues),
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
		case <-r.changed:
			if rest := time.Until(lastStart.Add(passGap(lastTook))); rest > 0 && !r.waitGap(ctx, rest, ps) {
				return
			}
		case <-wait:
		case job := <-r.jobs:
			r.restoreNow(ctx, ps, job)
		case done := <-r.signout:
			done <- r.signOutNow(ps)
		}
	}
}

func (r *Runner) waitGap(ctx context.Context, rest time.Duration, ps *passState) bool {
	select {
	case <-ctx.Done():
		return false
	case <-r.trigger:
	case <-time.After(rest):
	case done := <-r.signout:
		done <- r.signOutNow(ps)
	}
	return true
}

func (r *Runner) refreshSnapshot(ps *passState) {
	if ps.index == nil {
		return
	}
	rem, err := ps.index.snapshot()
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

const pendingPreview = 20
