package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

const (
	resumableMin    = 32 << 20
	resumableChunk  = 8 << 20
	sessionLifetime = 6 * 24 * time.Hour
	uploadEndpoint  = "https://www.googleapis.com/upload/drive/v3/files"
)

type uploadSession struct {
	URI     string    `json:"uri"`
	Created time.Time `json:"created"`
}

type sessionStore struct {
	mu   sync.Mutex
	path string
	m    map[string]uploadSession
}

func (p Paths) sessions() string { return filepath.Join(p.StateDir, "upload-sessions.json") }

func loadSessions(path string) *sessionStore {
	s := &sessionStore{path: path, m: map[string]uploadSession{}}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &s.m); err != nil {
			s.m = map[string]uploadSession{}
		}
	}
	for k, v := range s.m {
		if time.Since(v.Created) > sessionLifetime {
			delete(s.m, k)
		}
	}
	return s
}

func (s *sessionStore) get(key string) (uploadSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return v, ok
}

func (s *sessionStore) put(key string, v uploadSession) {
	s.mu.Lock()
	s.m[key] = v
	s.mu.Unlock()
	s.save()
}

func (s *sessionStore) drop(key string) {
	s.mu.Lock()
	delete(s.m, key)
	s.mu.Unlock()
	s.save()
}

func (s *sessionStore) save() {
	s.mu.Lock()
	b, err := json.Marshal(s.m)
	s.mu.Unlock()
	if err == nil {
		err = writeFileAtomic(s.path, b, 0o600)
	}
	if err != nil {
		log.Printf("save upload sessions: %v", err)
	}
}

type resumer struct {
	client   *http.Client
	sessions *sessionStore
	endpoint string
}

type uploadTarget struct {
	key    string
	fileID string
	newID  string
	name   string
	parent string
	mtime  time.Time
}

func sessionKey(rp string, lf LocalFile) string {
	return rp + "\x00" + strconv.FormatInt(lf.Size, 10) + "\x00" + strconv.FormatInt(lf.MTime, 10)
}

func (r *resumer) start(ctx context.Context, t uploadTarget, size int64) (string, error) {
	meta := map[string]any{"modifiedTime": t.mtime.UTC().Format(time.RFC3339Nano)}
	method, u := http.MethodPost, r.endpoint
	if t.fileID != "" {
		method, u = http.MethodPatch, r.endpoint+"/"+url.PathEscape(t.fileID)
	} else {
		meta["name"], meta["parents"] = t.name, []string{t.parent}
		if t.newID != "" {
			meta["id"] = t.newID
		}
	}
	body, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, u+"?uploadType=resumable&fields="+url.QueryEscape(fileFields), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := googleapi.CheckResponse(resp); err != nil {
		return "", err
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("upload session has no location")
	}
	return loc, nil
}

var errSessionGone = errors.New("upload session expired")

func parseRangeEnd(h string) int64 {
	_, end, ok := strings.Cut(strings.TrimPrefix(h, "bytes="), "-")
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(end, 10, 64)
	if err != nil {
		return 0
	}
	return n + 1
}

func (r *resumer) send(ctx context.Context, uri string, body io.Reader, length int64, contentRange string) (int64, *drive.File, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uri, body)
	if err != nil {
		return 0, nil, err
	}
	req.ContentLength = length
	req.Header.Set("Content-Range", contentRange)
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		f := &drive.File{}
		if err := json.NewDecoder(resp.Body).Decode(f); err != nil {
			return 0, nil, err
		}
		return 0, f, nil
	case resp.StatusCode == http.StatusPermanentRedirect:
		return parseRangeEnd(resp.Header.Get("Range")), nil, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return 0, nil, errSessionGone
	}
	return 0, nil, googleapi.CheckResponse(resp)
}

func (r *resumer) upload(ctx context.Context, t uploadTarget, full string, size int64, lim *limiter, onProgress func(int64)) (*drive.File, error) {
	var offset int64
	sess, ok := r.sessions.get(t.key)
	if ok {
		next, done, err := r.send(ctx, sess.URI, nil, 0, fmt.Sprintf("bytes */%d", size))
		if errors.Is(err, errSessionGone) {
			r.sessions.drop(t.key)
			return nil, fmt.Errorf("%w; starting over next try", err)
		}
		if err != nil {
			return nil, err
		}
		if done != nil {
			r.sessions.drop(t.key)
			return done, nil
		}
		offset = next
	} else {
		uri, err := r.start(ctx, t, size)
		if err != nil {
			return nil, err
		}
		sess = uploadSession{URI: uri, Created: time.Now()}
		r.sessions.put(t.key, sess)
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	for {
		if onProgress != nil {
			onProgress(offset)
		}
		n := min(int64(resumableChunk), size-offset)
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
		chunk := limitReader(ctx, io.LimitReader(f, n), lim)
		next, done, err := r.send(ctx, sess.URI, chunk, n, fmt.Sprintf("bytes %d-%d/%d", offset, offset+n-1, size))
		if errors.Is(err, errSessionGone) {
			r.sessions.drop(t.key)
		}
		if err != nil {
			return nil, err
		}
		if done != nil {
			r.sessions.drop(t.key)
			return done, nil
		}
		if next <= offset {
			return nil, &googleapi.Error{Code: http.StatusServiceUnavailable, Message: "upload made no progress"}
		}
		offset = next
	}
}
