package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"path"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

const (
	folderMime  = "application/vnd.google-apps.folder"
	gappsPrefix = "application/vnd.google-apps."
	fileFields  = "id,md5Checksum,modifiedTime,size"
)

var docExts = map[string]string{
	gappsPrefix + "document":     ".gdoc",
	gappsPrefix + "spreadsheet":  ".gsheet",
	gappsPrefix + "presentation": ".gslides",
	gappsPrefix + "drawing":      ".gdraw",
	gappsPrefix + "form":         ".gform",
	gappsPrefix + "map":          ".gmap",
	gappsPrefix + "site":         ".gsite",
}

func isLinkFile(name string) bool {
	ext := path.Ext(name)
	for _, e := range docExts {
		if e == ext {
			return true
		}
	}
	return false
}

type RemoteFile struct {
	ID    string
	MD5   string
	Size  int64
	MTime int64
}

type RemoteDoc struct {
	ID  string
	URL string
}

type Remote struct {
	Files     map[string]RemoteFile
	Docs      map[string]RemoteDoc
	Folders   map[string]string
	Ambiguous map[string]bool
	Dupes     map[string][]*drive.File
	Invalid   []string

	mu      sync.Mutex
	pending map[string]*folderCall
	ids     []string
	idFor   map[string]string
	journal *idJournal
	index   *driveIndex
}

func (r *Remote) clone() *Remote {
	return &Remote{Files: maps.Clone(r.Files), Docs: maps.Clone(r.Docs), Folders: maps.Clone(r.Folders), Ambiguous: maps.Clone(r.Ambiguous), Dupes: maps.Clone(r.Dupes), Invalid: slices.Clone(r.Invalid)}
}

func (r *Remote) releaseID(key string) {
	r.mu.Lock()
	delete(r.idFor, key)
	r.mu.Unlock()
	if err := r.journal.drop(key); err != nil {
		log.Printf("forget Drive id for %s: %v", key, err)
	}
}

const idBatch = 100

func (r *Remote) newID(ctx context.Context, srv *drive.Service, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.idFor[key]; ok {
		return id, nil
	}
	if id, ok := r.journal.get(key); ok {
		if r.idFor == nil {
			r.idFor = map[string]string{}
		}
		r.idFor[key] = id
		return id, nil
	}
	if len(r.ids) == 0 {
		var res *drive.GeneratedIds
		err := retry(ctx, func() error {
			var err error
			res, err = srv.Files.GenerateIds().Count(idBatch).Space("drive").Context(ctx).Do()
			return err
		})
		if err != nil {
			return "", err
		}
		if len(res.Ids) == 0 {
			return "", errors.New("Google Drive returned no file ids")
		}
		r.ids = res.Ids
	}
	if r.idFor == nil {
		r.idFor = map[string]string{}
	}
	id := r.ids[len(r.ids)-1]
	r.ids = r.ids[:len(r.ids)-1]
	r.idFor[key] = id
	if err := r.journal.put(key, id); err != nil {
		return "", err
	}
	return id, nil
}

func alreadyCreated(err error) bool {
	var g *googleapi.Error
	return errors.As(err, &g) && g.Code == http.StatusConflict
}

var errAdoptStale = errors.New("the file was created earlier with older content")

var errAdoptTrashed = errors.New("the file was created earlier but is now in the trash; it will be uploaded again")

func adoptCreated(ctx context.Context, srv *drive.Service, id, wantMD5 string) (*drive.File, error) {
	f, err := srv.Files.Get(id).Fields(fileFields + ",trashed").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	if f.Trashed {
		return nil, errAdoptTrashed
	}
	if wantMD5 != "" && f.Md5Checksum != wantMD5 {
		return f, errAdoptStale
	}
	return f, nil
}

type folderCall struct {
	done chan struct{}
	id   string
	err  error
}

func (r *Remote) blocked(p string) bool {
	if len(r.Ambiguous) == 0 {
		return false
	}
	for q := p; q != "." && q != ""; q = path.Dir(q) {
		if r.Ambiguous[foldKey(q)] {
			return true
		}
	}
	return false
}

func validName(name string) bool { return validNameFor(runtime.GOOS, name) }

func foldKey(p string) string {
	if caseInsensitiveFS(runtime.GOOS) {
		return strings.ToLower(p)
	}
	return p
}

const remoteFields = "id,name,mimeType,parents,md5Checksum,modifiedTime,size,webViewLink,ownedByMe,capabilities/canRename,version"

func rootFolderID(ctx context.Context, srv *drive.Service) (string, error) {
	var id string
	err := retryRead(ctx, func() error {
		root, err := srv.Files.Get("root").Fields("id").Context(ctx).Do()
		if err == nil {
			id = root.Id
		}
		return err
	})
	if err == nil && id == "" {
		err = errors.New("Google Drive did not return the My Drive folder id")
	}
	return id, err
}

func listAll(ctx context.Context, srv *drive.Service) (map[string]*drive.File, error) {
	all := map[string]*drive.File{}
	err := srv.Files.List().Q("trashed=false").Spaces("drive").PageSize(1000).
		Fields("nextPageToken,files("+remoteFields+")").
		Pages(ctx, func(l *drive.FileList) error {
			for _, f := range l.Files {
				all[f.Id] = f
			}
			return nil
		})
	return all, err
}

func listRemote(ctx context.Context, srv *drive.Service) (*Remote, error) {
	rootID, err := rootFolderID(ctx, srv)
	if err != nil {
		return nil, err
	}
	all, err := listAll(ctx, srv)
	if err != nil {
		return nil, err
	}
	return buildRemote(all, rootID)
}

func buildRemote(all map[string]*drive.File, rootID string) (*Remote, error) {
	paths := map[string]string{}
	if rootID != "" {
		paths[rootID] = ""
		paths["root"] = ""
	}
	var resolve func(id string, depth int) (string, bool)
	resolve = func(id string, depth int) (string, bool) {
		if p, ok := paths[id]; ok {
			return p, true
		}
		f, ok := all[id]
		if !ok || len(f.Parents) == 0 || depth > 64 || !validName(f.Name) {
			return "", false
		}
		if _, visible := all[f.Parents[0]]; rootID == "" && !visible && f.MimeType == folderMime && (f.Name == backupRootName || f.Name == syncRootName) {
			paths[id] = f.Name
			return f.Name, true
		}
		parent, ok := resolve(f.Parents[0], depth+1)
		if !ok {
			return "", false
		}
		p := path.Join(parent, f.Name)
		paths[id] = p
		return p, true
	}

	byPath := map[string][]*drive.File{}
	for id, f := range all {
		p, ok := resolve(id, 0)
		if !ok || p == "" {
			continue
		}
		byPath[p+docExts[f.MimeType]] = append(byPath[p+docExts[f.MimeType]], f)
	}

	r := &Remote{
		Files:     map[string]RemoteFile{},
		Docs:      map[string]RemoteDoc{},
		Folders:   map[string]string{"": "root"},
		Ambiguous: map[string]bool{},
	}
	folded := map[string]int{}
	for p, fs := range byPath {
		folded[foldKey(p)] += len(fs)
	}
	for k, n := range folded {
		if n > 1 {
			r.Ambiguous[k] = true
		}
	}
	for _, f := range all {
		if validName(f.Name) || len(f.Parents) == 0 {
			continue
		}
		parent, ok := resolve(f.Parents[0], 0)
		if !ok {
			continue
		}
		p := path.Join(parent, f.Name)
		r.Ambiguous[foldKey(p)] = true
		r.Invalid = append(r.Invalid, p)
	}
	sort.Strings(r.Invalid)
	for p, fs := range byPath {
		if k := foldKey(p); r.Ambiguous[k] {
			if r.Dupes == nil {
				r.Dupes = map[string][]*drive.File{}
			}
			r.Dupes[k] = append(r.Dupes[k], fs...)
		}
	}
	for p, fs := range byPath {
		if r.blocked(p) {
			continue
		}
		f := fs[0]
		switch {
		case f.MimeType == folderMime:
			r.Folders[p] = f.Id
		case docExts[f.MimeType] != "":
			r.Docs[p] = RemoteDoc{ID: f.Id, URL: f.WebViewLink}
		case strings.HasPrefix(f.MimeType, gappsPrefix), f.Md5Checksum == "":
		default:
			t, err := time.Parse(time.RFC3339, f.ModifiedTime)
			if err != nil {
				log.Printf("drive file %s: unreadable modified time %q; skipped: %v", p, f.ModifiedTime, err)
				r.Ambiguous[foldKey(p)] = true
				continue
			}
			r.Files[p] = RemoteFile{ID: f.Id, MD5: f.Md5Checksum, Size: f.Size, MTime: t.UnixNano()}
		}
	}
	return r, nil
}

func (r *Remote) invalidNameErrors() []error {
	errs := make([]error, 0, len(r.Invalid))
	for _, p := range r.Invalid {
		errs = append(errs, fmt.Errorf("rename %q on Google Drive: its name cannot be used on this computer, so it and everything inside it is not synced", p))
	}
	return errs
}

func (r *Remote) sub(prefix, id string) *Remote {
	out := &Remote{
		Files:     map[string]RemoteFile{},
		Docs:      map[string]RemoteDoc{},
		Folders:   map[string]string{"": id},
		Ambiguous: map[string]bool{},
	}
	pre := prefix + "/"
	for p, f := range r.Files {
		if rel, ok := strings.CutPrefix(p, pre); ok {
			out.Files[rel] = f
		}
	}
	for p, d := range r.Docs {
		if rel, ok := strings.CutPrefix(p, pre); ok {
			out.Docs[rel] = d
		}
	}
	for p, fid := range r.Folders {
		if rel, ok := strings.CutPrefix(p, pre); ok {
			out.Folders[rel] = fid
		}
	}
	for k := range r.Ambiguous {
		if rel, ok := strings.CutPrefix(k, foldKey(pre)); ok {
			out.Ambiguous[rel] = true
		}
	}
	for _, p := range r.Invalid {
		if rel, ok := strings.CutPrefix(p, pre); ok {
			out.Invalid = append(out.Invalid, rel)
		}
	}
	for k, fs := range r.Dupes {
		if rel, ok := strings.CutPrefix(k, foldKey(pre)); ok {
			if out.Dupes == nil {
				out.Dupes = map[string][]*drive.File{}
			}
			out.Dupes[rel] = fs
		}
	}
	return out
}
