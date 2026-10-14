package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aead.dev/minisign"
)

func TestNewerVersion(t *testing.T) {
	cases := map[[2]string]bool{
		{"v0.2.0", "v0.1.9"}:     true,
		{"v1.0.0", "v0.9.9"}:     true,
		{"v0.1.0", "v0.1.0"}:     false,
		{"v0.1.0", "v0.2.0"}:     false,
		{"v0.10.0", "v0.9.0"}:    true,
		{"v0.2.0", "dev"}:        false,
		{"junk", "v0.1.0"}:       false,
		{"v0.2.0-rc1", "v0.1.0"}: true,
	}
	for in, want := range cases {
		if got := newerVersion(in[0], in[1]); got != want {
			t.Errorf("newerVersion(%q, %q) = %v", in[0], in[1], got)
		}
	}
}

func tarGz(t *testing.T, name string, body []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func releaseServer(t *testing.T, archive []byte, name string, sign func([]byte) []byte) *httptest.Server {
	sum := sha256.Sum256(archive)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"tag_name":"v0.2.0"}`)) })
	mux.HandleFunc("/dl/v0.2.0/checksums.txt", func(w http.ResponseWriter, r *http.Request) { w.Write(sums) })
	mux.HandleFunc("/dl/v0.2.0/checksums.txt.minisig", func(w http.ResponseWriter, r *http.Request) { w.Write(sign(sums)) })
	mux.HandleFunc("/dl/v0.2.0/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdaterVerifiesSignature(t *testing.T) {
	pub, priv, _ := minisign.GenerateKey(rand.Reader)
	_, other, _ := minisign.GenerateKey(rand.Reader)
	pubText, _ := pub.MarshalText()
	name := assetName("linux", "amd64")
	archive := tarGz(t, "gobridgesync", []byte("new binary"))

	good := releaseServer(t, archive, name, func(m []byte) []byte { return minisign.Sign(priv, m) })
	u := &updater{client: good.Client(), api: good.URL + "/latest", files: good.URL + "/dl/", pubKey: string(pubText), goos: "linux", goarch: "amd64", current: "v0.1.0"}
	tag, err := u.available(context.Background())
	if err != nil || tag != "v0.2.0" {
		t.Fatalf("available: %q %v", tag, err)
	}
	bin, err := u.download(context.Background(), tag)
	if err != nil || string(bin) != "new binary" {
		t.Fatalf("download: %q %v", bin, err)
	}

	forged := releaseServer(t, archive, name, func(m []byte) []byte { return minisign.Sign(other, m) })
	u.api, u.files = forged.URL+"/latest", forged.URL+"/dl/"
	if _, err := u.download(context.Background(), "v0.2.0"); err == nil {
		t.Fatal("update signed with the wrong key was accepted")
	}

	u.current = "dev"
	if tag, _ := u.available(context.Background()); tag != "" {
		t.Fatal("dev builds must not update")
	}
}

func TestExtractWindowsZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("gobridgesync.exe")
	w.Write([]byte("exe"))
	zw.Close()
	got, err := extractBinary(buf.Bytes(), "windows")
	if err != nil || string(got) != "exe" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestReplaceExecutable(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "gobridgesync")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := replaceExecutable(exe, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("got %q", b)
	}
}
