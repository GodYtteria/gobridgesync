package main

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"path"
	"runtime"
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

	mu      sync.Mutex
	pending map[string]*folderCall
	ids     []string
	idFor   map[string]string
	journal *idJournal
}

func (r *Remote) clone() *Remote {
	return &Remote{Files: maps.Clone(r.Files), Docs: maps.Clone(r.Docs), Folders: maps.Clone(r.Folders), Ambiguous: maps.Clone(r.Ambiguous)}
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

const remoteFields = "id,name,mimeType,parents,md5Checksum,modifiedTime,size,webViewLink"

func rootFolderID(ctx context.Context, srv *drive.Service) string {
	if root, err := srv.Files.Get("root").Fields("id").Context(ctx).Do(); err == nil {
		return root.Id
	}
	return ""
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
	rootID := rootFolderID(ctx, srv)
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
				return nil, err
			}
			r.Files[p] = RemoteFile{ID: f.Id, MD5: f.Md5Checksum, Size: f.Size, MTime: t.UnixNano()}
		}
	}
	return r, nil
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
	return out
}
