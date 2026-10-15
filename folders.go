package main

import (
	"context"
	"errors"
	"path"

	"google.golang.org/api/drive/v3"
)

func (s *Syncer) folder(ctx context.Context, dir string) (string, error) {
	return s.remote.ensureFolder(ctx, s.srv, dir)
}

func (r *Remote) ensureFolder(ctx context.Context, srv *drive.Service, dir string) (string, error) {
	if dir == "." {
		dir = ""
	}
	r.mu.Lock()
	if id, ok := r.Folders[dir]; ok {
		r.mu.Unlock()
		return id, nil
	}
	if r.pending == nil {
		r.pending = map[string]*folderCall{}
	}
	if c, ok := r.pending[dir]; ok {
		r.mu.Unlock()
		select {
		case <-c.done:
			return c.id, c.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	c := &folderCall{done: make(chan struct{})}
	r.pending[dir] = c
	r.mu.Unlock()

	parent, err := r.ensureFolder(ctx, srv, path.Dir(dir))
	var id string
	if err == nil {
		id, err = r.newID(ctx, srv, "folder:"+dir)
	}
	if err == nil {
		err = retry(ctx, func() error {
			_, cerr := srv.Files.Create(&drive.File{Id: id, Name: path.Base(dir), MimeType: folderMime, Parents: []string{parent}}).
				Fields("id").Context(ctx).Do()
			if alreadyCreated(cerr) {
				if _, cerr = adoptCreated(ctx, srv, id, ""); errors.Is(cerr, errAdoptTrashed) {
					r.journal.drop("folder:" + dir)
					r.mu.Lock()
					delete(r.idFor, "folder:"+dir)
					r.mu.Unlock()
				}
			}
			if cerr == nil {
				c.id = id
			}
			return cerr
		})
	}
	c.err = err
	r.mu.Lock()
	if err == nil {
		r.Folders[dir] = c.id
	}
	delete(r.pending, dir)
	r.mu.Unlock()
	close(c.done)
	return c.id, err
}
