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
	"time"

	"google.golang.org/api/drive/v3"
)

const (
	inboxName    = "Inbox"
	inboxSettle  = 10 * time.Second
	largeModel   = 100 << 20
	foldersShelf = "Folders"
)

var monthly = map[string]bool{"Pictures": true, "Videos": true}

var categories = map[string][]string{
	"Pictures":   {"jpg", "jpeg", "png", "gif", "webp", "heic", "heif", "avif", "bmp", "tif", "tiff", "svg", "raw", "cr2", "nef", "arw", "dng"},
	"Videos":     {"mp4", "mov", "mkv", "avi", "webm", "m4v", "3gp"},
	"Music":      {"mp3", "flac", "m4a", "aac", "wav", "ogg", "opus"},
	"Documents":  {"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "odp", "txt", "md", "rtf", "csv", "gdoc", "gsheet", "gslides", "gdraw", "gform"},
	"Ebooks":     {"epub", "mobi", "azw3"},
	"Archives":   {"zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar", "zst"},
	"Installers": {"exe", "msi", "dmg", "pkg", "rpm", "deb", "appimage", "flatpakref", "iso", "img"},
	"Models":     {"gguf", "safetensors", "onnx", "pt", "pth", "ckpt"},
	"Code":       {"go", "js", "ts", "py", "rs", "java", "c", "cpp", "h", "sh", "rb", "php", "kt", "swift", "sql", "json", "yaml", "yml", "toml", "html", "css"},
	"Design":     {"psd", "ai", "fig", "sketch", "xcf", "kra", "blend"},
	"3D":         {"stl", "obj", "fbx", "gltf", "glb", "3mf"},
	"Fonts":      {"ttf", "otf", "woff", "woff2"},
}

var extCategory = func() map[string]string {
	m := map[string]string{}
	for cat, exts := range categories {
		for _, e := range exts {
			m[e] = cat
		}
	}
	return m
}()

func categoryFor(name string, size int64, mtime time.Time, dir bool) string {
	if dir {
		return foldersShelf
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	cat := extCategory[ext]
	if ext == "bin" && size >= largeModel {
		cat = "Models"
	}
	switch {
	case cat == "" && ext == "":
		return "Other"
	case cat == "":
		return "Other/" + strings.ToUpper(ext)
	case monthly[cat]:
		return cat + "/" + mtime.Local().Format("2006-01")
	}
	return cat
}

func incomplete(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range []string{".part", ".crdownload", ".download", ".tmp", ".partial"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".~lock") || strings.HasPrefix(name, tmpPrefix)
}

func recentlyChanged(full string, now time.Time) bool {
	recent := false
	filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
		if err != nil || recent {
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && now.Sub(info.ModTime()) < inboxSettle {
			recent = true
			return filepath.SkipAll
		}
		return nil
	})
	return recent
}

type inboxItem struct {
	name  string
	dir   bool
	local bool
	id    string
	doc   bool
	size  int64
	mtime time.Time
}

func (s *Syncer) inboxItems(remote *Remote, now time.Time) []inboxItem {
	items := map[string]*inboxItem{}
	get := func(name string) *inboxItem {
		if items[name] == nil {
			items[name] = &inboxItem{name: name}
		}
		return items[name]
	}
	prefix := inboxName + "/"
	for p, f := range remote.Files {
		if rest, ok := strings.CutPrefix(p, prefix); ok && !strings.Contains(rest, "/") && !remote.blocked(p) {
			it := get(rest)
			it.id, it.size, it.mtime = f.ID, f.Size, time.Unix(0, f.MTime)
		}
	}
	for p, d := range remote.Docs {
		if rest, ok := strings.CutPrefix(p, prefix); ok && !strings.Contains(rest, "/") && !remote.blocked(p) {
			it := get(rest)
			it.id, it.doc = d.ID, true
		}
	}
	for p, id := range remote.Folders {
		if rest, ok := strings.CutPrefix(p, prefix); ok && !strings.Contains(rest, "/") && !remote.blocked(p) {
			it := get(rest)
			it.id, it.dir = id, true
		}
	}
	entries, _ := os.ReadDir(s.abs(inboxName))
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 || !validName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		it := get(e.Name())
		it.local, it.dir, it.size, it.mtime = true, e.IsDir(), info.Size(), info.ModTime()
		if it.id == "" && !it.dir {
			if entry, ok := s.st.Files[inboxName+"/"+e.Name()]; ok {
				it.id = entry.ID
			}
		}
	}
	out := make([]inboxItem, 0, len(items))
	for _, it := range items {
		if incomplete(it.name) || isLinkFile(it.name) && !it.doc {
			continue
		}
		if it.local && recentlyChanged(s.abs(inboxName+"/"+it.name), now) {
			continue
		}
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func taken(remote *Remote, st *State, rel, full string) bool {
	if _, err := os.Lstat(full); err == nil {
		return true
	}
	_, f := remote.Files[rel]
	_, d := remote.Folders[rel]
	_, g := remote.Docs[rel]
	_, e := st.Files[rel]
	return f || d || g || e
}

func (s *Syncer) freeTarget(remote *Remote, dir, name string) string {
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := name
	for n := 2; taken(remote, s.st, path.Join(dir, candidate), s.abs(path.Join(dir, candidate))); n++ {
		candidate = fmt.Sprintf("%s (%d)%s", base, n, ext)
	}
	return path.Join(dir, candidate)
}

func rekey[V any](m map[string]V, from, to string, tree bool) {
	if v, ok := m[from]; ok {
		delete(m, from)
		m[to] = v
	}
	if !tree {
		return
	}
	for k, v := range m {
		if rest, ok := strings.CutPrefix(k, from+"/"); ok {
			delete(m, k)
			m[to+"/"+rest] = v
		}
	}
}

func (s *Syncer) sortInbox(ctx context.Context, remote *Remote) []error {
	if err := os.MkdirAll(s.abs(inboxName), 0o755); err != nil {
		return []error{fmt.Errorf("inbox: %w", err)}
	}
	inboxID, err := remote.ensureFolder(ctx, s.srv, inboxName)
	if err != nil {
		return []error{fmt.Errorf("inbox: %w", err)}
	}
	where := map[string]string{}
	for p, f := range remote.Files {
		where[f.ID] = p
	}
	for p, d := range remote.Docs {
		where[d.ID] = p
	}
	for p, id := range remote.Folders {
		where[id] = p
	}
	var errs []error
	for _, it := range s.inboxItems(remote, time.Now()) {
		if ctx.Err() != nil {
			break
		}
		src := inboxName + "/" + it.name
		if moved, ok := where[it.id]; it.id != "" && ok && moved != src {
			if err := s.followMove(src, moved); err != nil {
				errs = append(errs, fmt.Errorf("sort %s: %w", it.name, err))
			}
			s.dirty = true
			continue
		}
		if it.id != "" && it.local && !it.dir {
			if _, ok := where[it.id]; !ok {
				continue
			}
		}
		dir := categoryFor(it.name, it.size, it.mtime, it.dir)
		dst := s.freeTarget(remote, dir, it.name)
		if err := s.moveItem(ctx, remote, it, inboxID, src, dst); err != nil {
			errs = append(errs, fmt.Errorf("sort %s: %w", it.name, err))
			continue
		}
		s.dirty = true
		if s.notify != nil {
			s.notify("sorted " + it.name + " → " + path.Dir(dst))
		}
	}
	return errs
}

func (s *Syncer) moveItem(ctx context.Context, remote *Remote, it inboxItem, inboxID, src, dst string) error {
	renamed := false
	if it.local {
		if err := os.MkdirAll(filepath.Dir(s.abs(dst)), 0o755); err != nil {
			return err
		}
		err := os.Rename(s.abs(src), s.abs(dst))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		renamed = err == nil
	}
	if it.id != "" {
		parent, err := remote.ensureFolder(ctx, s.srv, path.Dir(dst))
		if err == nil {
			name := path.Base(dst)
			if it.doc {
				name = strings.TrimSuffix(name, path.Ext(name))
			}
			err = retry(ctx, func() error {
				_, err := s.srv.Files.Update(it.id, &drive.File{Name: name}).AddParents(parent).RemoveParents(inboxID).Fields("id").Context(ctx).Do()
				return err
			})
			if err == nil {
				s.index.moved(it.id, name, parent)
			}
		}
		if err != nil {
			if renamed {
				if rerr := os.Rename(s.abs(dst), s.abs(src)); rerr != nil {
					return errors.Join(err, fmt.Errorf("put back %s: %w", src, rerr))
				}
			}
			return err
		}
	}
	tree := it.dir
	rekey(s.st.Files, src, dst, tree)
	rekey(s.st.Links, src, dst, tree)
	rekey(remote.Files, src, dst, tree)
	rekey(remote.Docs, src, dst, tree)
	rekey(remote.Folders, src, dst, tree)
	return nil
}

func (s *Syncer) followMove(src, dst string) error {
	if _, err := os.Lstat(s.abs(dst)); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.abs(dst)), 0o755); err != nil {
		return err
	}
	if err := os.Rename(s.abs(src), s.abs(dst)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	info, err := os.Stat(s.abs(dst))
	tree := err == nil && info.IsDir()
	rekey(s.st.Files, src, dst, tree)
	rekey(s.st.Links, src, dst, tree)
	return nil
}
