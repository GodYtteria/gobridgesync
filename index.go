package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

const fullListEvery = 24 * time.Hour

type driveIndex struct {
	path   string
	RootID string                 `json:"rootId"`
	Token  string                 `json:"token"`
	FullAt time.Time              `json:"fullAt"`
	Files  map[string]*drive.File `json:"files"`
}

func (p Paths) index() string { return filepath.Join(p.StateDir, "drive-index.json") }

func loadIndex(path string) *driveIndex {
	x := &driveIndex{path: path}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, x); err != nil {
			log.Printf("drive index unreadable, rebuilding: %v", err)
			x = &driveIndex{path: path}
		}
	}
	if x.Files == nil {
		x.Files = map[string]*drive.File{}
	}
	return x
}

func (x *driveIndex) save() error {
	b, err := json.Marshal(x)
	if err != nil {
		return err
	}
	return writeFileAtomic(x.path, b, 0o600)
}

func tokenExpired(err error) bool {
	var g *googleapi.Error
	return errors.As(err, &g) && (g.Code == http.StatusNotFound || g.Code == http.StatusGone || g.Code == http.StatusBadRequest)
}

func (x *driveIndex) remote(ctx context.Context, srv *drive.Service) (*Remote, error) {
	var err error
	if x.Token == "" || time.Since(x.FullAt) > fullListEvery {
		err = x.full(ctx, srv)
	} else if err = x.changes(ctx, srv); tokenExpired(err) {
		err = x.full(ctx, srv)
	}
	if err != nil {
		return nil, err
	}
	if err := x.save(); err != nil {
		log.Printf("save drive index: %v", err)
	}
	return buildRemote(x.Files, x.RootID)
}

func (x *driveIndex) full(ctx context.Context, srv *drive.Service) error {
	start, err := srv.Changes.GetStartPageToken().Context(ctx).Do()
	if err != nil {
		return err
	}
	all, err := listAll(ctx, srv)
	if err != nil {
		return err
	}
	x.RootID = rootFolderID(ctx, srv)
	x.Files, x.Token, x.FullAt = all, start.StartPageToken, time.Now()
	return nil
}

func (x *driveIndex) changes(ctx context.Context, srv *drive.Service) error {
	files := make(map[string]*drive.File, len(x.Files))
	for k, v := range x.Files {
		files[k] = v
	}
	token := x.Token
	for {
		page, err := srv.Changes.List(token).Spaces("drive").IncludeRemoved(true).PageSize(1000).
			Fields("nextPageToken,newStartPageToken,changes(fileId,removed,file(" + remoteFields + ",trashed))").
			Context(ctx).Do()
		if err != nil {
			return err
		}
		applyChanges(files, page.Changes)
		if page.NextPageToken != "" {
			token = page.NextPageToken
			continue
		}
		x.Files, x.Token = files, page.NewStartPageToken
		return nil
	}
}

func applyChanges(files map[string]*drive.File, changes []*drive.Change) {
	for _, c := range changes {
		if c.FileId == "" {
			continue
		}
		if c.Removed || c.File == nil || c.File.Trashed {
			delete(files, c.FileId)
			continue
		}
		c.File.Trashed = false
		files[c.FileId] = c.File
	}
}

func (x *driveIndex) remoteOrList(ctx context.Context, srv *drive.Service) (*Remote, error) {
	if x == nil {
		return listRemote(ctx, srv)
	}
	return x.remote(ctx, srv)
}
