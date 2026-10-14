package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"aead.dev/minisign"
)

var version = "dev"

const (
	updateRepo       = "GodYtteria/gobridgesync"
	releasePublicKey = "RWRa9VXSkFqxcvPoeZfaYlVJGCZ8hPuCRXqYjgzgMQpnb9tSP8sPjQf6"
	maxDownload      = 300 << 20
)

type updater struct {
	client  *http.Client
	api     string
	files   string
	pubKey  string
	goos    string
	goarch  string
	current string
}

func newUpdater() *updater {
	api, files := "https://api.github.com/repos/"+updateRepo+"/releases/latest", "https://github.com/"+updateRepo+"/releases/download/"
	if updateBase != "" {
		base := strings.TrimSuffix(updateBase, "/")
		api, files = base+"/latest.json", base+"/"
	}
	return &updater{
		client:  &http.Client{Timeout: 10 * time.Minute},
		api:     api,
		files:   files,
		pubKey:  releasePublicKey,
		goos:    runtime.GOOS,
		goarch:  runtime.GOARCH,
		current: version,
	}
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v, _, _ = strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func newerVersion(latest, current string) bool {
	l, ok1 := parseVersion(latest)
	c, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func assetName(goos, goarch string) string {
	if goos == "windows" {
		return "gobridgesync-windows-" + goarch + ".zip"
	}
	return "gobridgesync-" + goos + "-" + goarch + ".tar.gz"
}

func releaseName() string {
	if internalEdition() {
		return "GoBridgeSync Bukluran"
	}
	return "GoBridgeSync"
}

func (u *updater) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", path.Base(url), resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDownload {
		return nil, errors.New("download too large")
	}
	return b, nil
}

func (u *updater) latest(ctx context.Context) (string, error) {
	b, err := u.get(ctx, u.api)
	if err != nil {
		return "", err
	}
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(b, &rel); err != nil {
		return "", err
	}
	if _, ok := parseVersion(rel.Tag); !ok {
		return "", fmt.Errorf("unexpected release tag %q", rel.Tag)
	}
	return rel.Tag, nil
}

func (u *updater) available(ctx context.Context) (string, error) {
	if _, ok := parseVersion(u.current); !ok {
		return "", nil
	}
	tag, err := u.latest(ctx)
	if err != nil || !newerVersion(tag, u.current) {
		return "", err
	}
	return tag, nil
}

func verifiedChecksum(sums, sig []byte, pubKey, name string) (string, error) {
	var pub minisign.PublicKey
	if err := pub.UnmarshalText([]byte(pubKey)); err != nil {
		return "", err
	}
	if !minisign.Verify(pub, sums, sig) {
		return "", errors.New("release signature is not valid")
	}
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum for %s", name)
}

func (u *updater) download(ctx context.Context, tag string) ([]byte, error) {
	base := u.files + tag + "/"
	name := assetName(u.goos, u.goarch)
	sums, err := u.get(ctx, base+"checksums.txt")
	if err != nil {
		return nil, err
	}
	sig, err := u.get(ctx, base+"checksums.txt.minisig")
	if err != nil {
		return nil, err
	}
	want, err := verifiedChecksum(sums, sig, u.pubKey, name)
	if err != nil {
		return nil, err
	}
	archive, err := u.get(ctx, base+name)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != want {
		return nil, errors.New("downloaded update does not match its checksum")
	}
	return extractBinary(archive, u.goos)
}

func extractBinary(archive []byte, goos string) ([]byte, error) {
	if goos == "windows" {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.Name == "gobridgesync.exe" {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, maxDownload))
			}
		}
		return nil, errors.New("update archive has no gobridgesync.exe")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("update archive has no gobridgesync binary")
		}
		if err != nil {
			return nil, err
		}
		if path.Clean(h.Name) == "gobridgesync" && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

func replaceExecutable(exe string, bin []byte) error {
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.WriteFile(exe, bin, 0o755); err != nil {
			os.Rename(old, exe)
			return err
		}
		return nil
	}
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func canReplaceExecutable() bool {
	exe, err := executable()
	if err != nil {
		return false
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".gobridgesync-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func cleanupOldExecutable() {
	if runtime.GOOS != "windows" {
		return
	}
	if exe, err := executable(); err == nil {
		os.Remove(exe + ".old")
	}
}
