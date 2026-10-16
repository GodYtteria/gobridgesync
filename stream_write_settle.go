package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

func committedOnDrive(cur *drive.File, sum, sentMD5 string) bool {
	if cur.Trashed || cur.Md5Checksum == "" {
		return false
	}
	return cur.Md5Checksum == sum || (sentMD5 != "" && cur.Md5Checksum == sentMD5)
}

func sameMTime(driveMTime string, mt int64) bool {
	t, err := time.Parse(time.RFC3339Nano, driveMTime)
	if err != nil {
		return false
	}
	return t.UnixMilli() == mt/int64(time.Millisecond)
}

func (s *stageFile) conflictFor(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	first := s.conflict == ""
	if first {
		s.conflict = conflictFileName(name, time.Now())
	}
	return s.conflict, first
}

func (s *stageFile) isDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

func (w *streamWriter) settleUnchanged(ctx context.Context, sf *stageFile, p, id, name, parent string, cur *drive.File, version int64) (bool, error) {
	sf.mu.Lock()
	same := sf.version == version && sf.writers == 0
	mt, timeSet := sf.mtime, sf.timeSet
	sf.mu.Unlock()
	if !same {
		return false, nil
	}
	if timeSet && mt != 0 && !sameMTime(cur.ModifiedTime, mt) {
		fixed, err := w.setRemoteMTime(ctx, id, mt)
		if err != nil {
			return false, err
		}
		w.index.note(fixed, name, parent)
		w.changed()
		log.Printf("streaming: set the modified time of %s to %s (content unchanged)", p, time.Unix(0, mt).Format(time.RFC3339))
	}
	sf.mu.Lock()
	same = sf.version == version && sf.writers == 0
	if same {
		sf.done = true
	}
	sf.mu.Unlock()
	if same {
		w.discard(sf)
	}
	return same, nil
}

func (w *streamWriter) dropOrphan(ctx context.Context, sf *stageFile, p, id, idKey string) bool {
	if !sf.isDone() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), streamMetaTimeout)
	defer cancel()
	var cur *drive.File
	err := retryRead(ctx, func() (err error) {
		cur, err = w.srv.Files.Get(id).Fields("id,trashed").Context(ctx).Do()
		return err
	})
	var apiErr *googleapi.Error
	switch {
	case errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound:
	case err != nil:
		log.Printf("streaming: %s was deleted during saving; could not check whether Drive kept a copy (id %s): %v", p, id, err)
		return true
	case !cur.Trashed:
		if terr := w.trashRemote(ctx, id, ""); terr != nil {
			log.Printf("streaming: %s was deleted during saving; its copy on Drive (id %s) could not be moved to the trash: %v", p, id, terr)
			return true
		}
		w.index.forget(id)
		w.changed()
		log.Printf("streaming: %s was deleted during saving; moved its copy on Drive (id %s) to the trash", p, id)
	}
	w.ids.releaseID(idKey)
	return true
}
