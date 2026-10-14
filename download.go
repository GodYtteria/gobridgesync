package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

func partName(dir, id, revision, sum string) string {
	h := md5.Sum([]byte(id + "\x00" + revision + "\x00" + sum))
	return filepath.Join(dir, tmpPrefix+"part-"+hex.EncodeToString(h[:8]))
}

const stalePartAge = 7 * 24 * time.Hour

func stalePart(name string, modTime time.Time) bool {
	return strings.HasPrefix(name, tmpPrefix+"part-") && time.Since(modTime) > stalePartAge
}

func removeStalePart(p string, entry fs.DirEntry) {
	if info, err := entry.Info(); err == nil && stalePart(entry.Name(), info.ModTime()) {
		os.Remove(p)
	}
}

func cleanStaleParts(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), tmpPrefix+"part-") {
			removeStalePart(filepath.Join(dir, e.Name()), e)
		}
	}
}

func openDownload(ctx context.Context, srv *drive.Service, id, revision string, offset int64) (*http.Response, error) {
	rng := ""
	if offset > 0 {
		rng = "bytes=" + strconv.FormatInt(offset, 10) + "-"
	}
	if revision == "" {
		call := srv.Files.Get(id).Context(ctx)
		if rng != "" {
			call.Header().Set("Range", rng)
		}
		return call.Download()
	}
	call := srv.Revisions.Get(id, revision).Context(ctx)
	if rng != "" {
		call.Header().Set("Range", rng)
	}
	return call.Download()
}

func downloadFile(ctx context.Context, srv *drive.Service, id, revision, sum string, mtime int64, full string, lim *limiter) error {
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	part := partName(dir, id, revision, sum)
	var offset int64
	if info, err := os.Stat(part); err == nil && sum != "" {
		offset = info.Size()
	}
	resp, err := openDownload(ctx, srv, id, revision, offset)
	var gerr *googleapi.Error
	if offset > 0 && errors.As(err, &gerr) && gerr.Code == http.StatusRequestedRangeNotSatisfiable {
		offset = 0
		resp, err = openDownload(ctx, srv, id, revision, 0)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 && resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, limitReader(ctx, resp.Body, lim))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if sum != "" {
		got, err := fileMD5(part)
		if err != nil {
			return err
		}
		if got != sum {
			os.Remove(part)
			return fmt.Errorf("checksum mismatch: got %s want %s", got, sum)
		}
	}
	if err := os.Chmod(part, 0o644); err != nil {
		return err
	}
	if mtime != 0 {
		mt := time.Unix(0, mtime)
		if err := os.Chtimes(part, mt, mt); err != nil {
			return err
		}
	}
	return os.Rename(part, full)
}
