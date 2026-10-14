package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
)

const (
	defaultUIAddr  = "127.0.0.1:47811"
	maxCredentials = 64 << 10
)

//go:embed web
var webFS embed.FS

//go:embed assets/logo.png
var logoPNG []byte

var indexTmpl = template.Must(template.ParseFS(webFS, "web/index.html"))

type ui struct {
	runner *Runner
	paths  Paths
	addr   string
	token  string
	ctx    context.Context
}

func serveUI(ctx context.Context, paths Paths, force bool, addr string, browser, tray bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return errors.New("ui address must be a loopback IP")
	}
	url := "http://" + addr + "/"
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if browser && uiRunning(url) {
			return openBrowser(url)
		}
		return err
	}
	lf, err := lock(paths.StateDir)
	if err != nil {
		ln.Close()
		return err
	}
	defer lf.Close()

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cleanupOldExecutable()
	u := &ui{runner: newRunner(paths, force), paths: paths, addr: addr, token: hex.EncodeToString(buf), ctx: ctx}
	srv := &http.Server{Handler: u.routes(), ReadHeaderTimeout: 10 * time.Second}
	u.runner.restart = func() {
		restartRequested.Store(true)
		cancel()
	}
	ensureAutostart(paths)
	go u.runner.Loop(ctx)
	go u.runner.UpdateLoop(ctx)
	go u.runner.WatchLoop(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Printf("GoBridgeSync UI at %s", url)
	if browser {
		_ = openBrowser(url)
	}
	if tray {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		errCh := make(chan error, 1)
		go func() {
			errCh <- srv.Serve(ln)
			cancel()
		}()
		if err := runTray(ctx, cancel, u.runner, url); err != nil {
			return err
		}
		if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return ctx.Err()
}

func uiRunning(url string) bool {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url + "api/ping")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (u *ui) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", u.index)
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, u.runner.Snapshot()) })
	mux.HandleFunc("GET /logo.png", pngHandler(logoPNG))
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(mustSub(webFS, "web/static"))))
	mux.HandleFunc("POST /api/credentials", u.post(u.credentials))
	mux.HandleFunc("POST /api/settings", u.post(u.settings))
	mux.HandleFunc("POST /api/backups", u.jsonPost(func(r *http.Request) (any, error) {
		var in struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, errors.New("invalid request")
		}
		return u.runner.Browse(in.Path)
	}))
	mux.HandleFunc("POST /api/measure", u.jsonPost(func(r *http.Request) (any, error) {
		var in struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, errors.New("invalid request")
		}
		return u.runner.Measure(in.Path)
	}))
	mux.HandleFunc("POST /api/versions", u.jsonPost(func(r *http.Request) (any, error) {
		var in struct {
			FileID string `json:"fileId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.FileID == "" {
			return nil, errors.New("invalid request")
		}
		return u.runner.Versions(r.Context(), in.FileID)
	}))
	mux.HandleFunc("POST /api/restore", u.post(func(w http.ResponseWriter, r *http.Request) error {
		var job restoreJob
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&job); err != nil {
			return errors.New("invalid request")
		}
		return u.runner.QueueRestore(job)
	}))
	mux.HandleFunc("POST /api/signout", u.post(func(w http.ResponseWriter, r *http.Request) error { return u.runner.SignOut(r.Context()) }))
	mux.HandleFunc("POST /api/update", u.post(func(w http.ResponseWriter, r *http.Request) error { return u.runner.ApplyUpdate(u.ctx) }))
	mux.HandleFunc("POST /api/dirs", u.dirs)
	mux.HandleFunc("POST /api/clear-issues", u.post(func(w http.ResponseWriter, r *http.Request) error { u.runner.ClearIssues(); return nil }))
	mux.HandleFunc("POST /api/login", u.post(func(w http.ResponseWriter, r *http.Request) error { return u.runner.Login(u.ctx, "http://"+u.addr+"/") }))
	mux.HandleFunc("POST /api/sync", u.post(func(w http.ResponseWriter, r *http.Request) error { u.runner.Trigger(); return nil }))
	mux.HandleFunc("POST /api/pause", u.post(func(w http.ResponseWriter, r *http.Request) error { return u.runner.SetPaused(true) }))
	mux.HandleFunc("POST /api/resume", u.post(func(w http.ResponseWriter, r *http.Request) error { return u.runner.SetPaused(false) }))
	mux.HandleFunc("POST /api/open-folder", u.post(func(w http.ResponseWriter, r *http.Request) error {
		if err := os.MkdirAll(u.paths.Root, 0o755); err != nil {
			return err
		}
		return openPath(u.paths.Root)
	}))
	return u.guard(mux)
}

func (u *ui) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != u.addr {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (u *ui) post(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !u.authorized(w, r) {
			return
		}
		if err := fn(w, r); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, u.runner.Snapshot())
	}
}

func (u *ui) authorized(w http.ResponseWriter, r *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-GBS-Token")), []byte(u.token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+u.addr {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return false
	}
	return true
}

func (u *ui) jsonPost(fn func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !u.authorized(w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		v, err := fn(r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, v)
	}
}

func (u *ui) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, map[string]string{"Token": u.token}); err != nil {
		log.Printf("render ui: %v", err)
	}
}

func (u *ui) credentials(w http.ResponseWriter, r *http.Request) error {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCredentials))
	if err != nil {
		return fmt.Errorf("file too large or unreadable")
	}
	cfg, err := google.ConfigFromJSON(b, drive.DriveFileScope)
	if err != nil || cfg.ClientID == "" {
		return errors.New("this is not a Google OAuth client file; download the JSON for a Desktop app client")
	}
	if err := writeFileAtomic(u.paths.credentials(), b, 0o600); err != nil {
		return err
	}
	u.runner.addRecent("OAuth client saved")
	u.runner.Trigger()
	return nil
}

type settingsInput struct {
	Folders *[]string `json:"folders"`
	TwoWay  *bool     `json:"twoWay"`
	Limit   *int      `json:"storageLimit"`
	Every   *int      `json:"intervalMinutes"`
	Startup *bool     `json:"startAtLogin"`
	Updates *bool     `json:"autoUpdate"`
	Exclude *[]string `json:"exclude"`
	Speed   *int      `json:"speedLimitKB"`
}

func applySettings(cfg AppConfig, in settingsInput, syncRoot string) (AppConfig, error) {
	known := cfg.Folders
	if in.Limit != nil && !storageLimits[*in.Limit] {
		return cfg, errors.New("storage limit must be 50, 75, 90, or 100 percent")
	}
	if in.Every != nil && !syncIntervals[*in.Every] {
		return cfg, errors.New("sync must be instant, manual, or every 1, 5, 15, or 60 minutes")
	}
	if in.Folders != nil {
		folders, err := normalizeFolders(*in.Folders, syncRoot, known)
		if err != nil {
			return cfg, err
		}
		cfg.Folders = folders
	}
	if in.TwoWay != nil {
		cfg.TwoWay = *in.TwoWay
	}
	if in.Limit != nil {
		cfg.Limit = *in.Limit
	}
	if in.Every != nil {
		cfg.Every = *in.Every
	}
	if in.Startup != nil {
		cfg.Startup = in.Startup
	}
	if in.Updates != nil {
		cfg.Updates = in.Updates
	}
	if in.Speed != nil {
		if !speedLimits[*in.Speed] {
			return cfg, errors.New("speed limit must be unlimited, 512 KB/s, 1, 5, or 10 MB/s")
		}
		cfg.Speed = *in.Speed
	}
	if in.Exclude != nil {
		ex, err := normalizeExcludes(*in.Exclude)
		if err != nil {
			return cfg, err
		}
		cfg.Exclude = ex
	}
	return cfg, nil
}

func (u *ui) settings(w http.ResponseWriter, r *http.Request) error {
	var in settingsInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCredentials)).Decode(&in); err != nil {
		return errors.New("invalid settings")
	}
	var before *bool
	_, err := editConfig(u.paths, func(c *AppConfig) error {
		next, err := applySettings(*c, in, u.paths.Root)
		if err != nil {
			return err
		}
		before = c.Startup
		*c = next
		return nil
	})
	if err != nil {
		return err
	}
	if in.Startup != nil {
		if err := setAutostart(*in.Startup); err != nil {
			_, revertErr := editConfig(u.paths, func(c *AppConfig) error {
				c.Startup = before
				return nil
			})
			return errors.Join(fmt.Errorf("start at login: %w", err), revertErr)
		}
	}
	u.runner.addRecent("settings saved")
	u.runner.Trigger()
	return nil
}

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type dirListing struct {
	Path   string     `json:"path"`
	Parent string     `json:"parent"`
	Dirs   []dirEntry `json:"dirs"`
}

const maxDirEntries = 2000

func listDirs(raw string) (dirListing, error) {
	dir, err := expandHome(strings.TrimSpace(raw))
	if err != nil {
		return dirListing{}, err
	}
	if dir == "" {
		if dir, err = os.UserHomeDir(); err != nil {
			return dirListing{}, err
		}
	}
	if !filepath.IsAbs(dir) {
		return dirListing{}, errors.New("use a full path like ~/Documents")
	}
	dir = filepath.Clean(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dirListing{}, fmt.Errorf("cannot open %s", dir)
	}
	out := dirListing{Path: dir, Dirs: []dirEntry{}}
	if parent := filepath.Dir(dir); parent != dir {
		out.Parent = parent
	}
	for _, e := range entries {
		if len(out.Dirs) == maxDirEntries {
			break
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			out.Dirs = append(out.Dirs, dirEntry{Name: e.Name(), Path: filepath.Join(dir, e.Name())})
		}
	}
	sort.Slice(out.Dirs, func(i, j int) bool { return strings.ToLower(out.Dirs[i].Name) < strings.ToLower(out.Dirs[j].Name) })
	return out, nil
}

func (u *ui) dirs(w http.ResponseWriter, r *http.Request) {
	if !u.authorized(w, r) {
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	listing, err := listDirs(in.Path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, listing)
}

func pngHandler(b []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(b)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
