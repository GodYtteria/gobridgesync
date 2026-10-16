package main

import (
	"testing"

	"google.golang.org/api/drive/v3"
)

func TestBuildRemoteMapsNamesThisComputerCannotUse(t *testing.T) {
	all := map[string]*drive.File{
		"bad":   {Id: "bad", Name: "a/b", MimeType: folderMime, Parents: []string{"root"}},
		"child": {Id: "child", Name: "c.txt", Parents: []string{"bad"}, Md5Checksum: "m", ModifiedTime: "2026-01-01T00:00:00Z"},
		"ok":    {Id: "ok", Name: "fine.txt", Parents: []string{"root"}, Md5Checksum: "m", ModifiedTime: "2026-01-01T00:00:00Z"},
	}
	r, err := buildRemote(all, "root")
	if err != nil {
		t.Fatal(err)
	}
	if r.Folders["a_b"] != "bad" {
		t.Fatalf("a Drive folder with a slash must sync under a safe local name: %v", r.Folders)
	}
	if f, ok := r.Files["a_b/c.txt"]; !ok || f.ID != "child" {
		t.Fatal("files inside a renamed-for-this-computer folder must still sync")
	}
	if _, ok := r.Files["fine.txt"]; !ok {
		t.Fatal("valid file lost")
	}
	if errs := r.invalidNameErrors(); len(errs) != 0 {
		t.Fatalf("a name that can be mapped is not a problem: %v", errs)
	}
}

func TestBuildRemoteMappedNameThatCollidesIsBlocked(t *testing.T) {
	all := map[string]*drive.File{
		"x": {Id: "x", Name: "a/b", MimeType: folderMime, Parents: []string{"root"}},
		"y": {Id: "y", Name: "a_b", MimeType: folderMime, Parents: []string{"root"}},
	}
	r, err := buildRemote(all, "root")
	if err != nil {
		t.Fatal(err)
	}
	if !r.blocked("a_b") {
		t.Fatal("two Drive names that map to the same local name must be blocked, not merged")
	}
}

func TestLocalNameForWindows(t *testing.T) {
	cases := map[string]string{
		"BS: PR, Media, Marketing": "BS_ PR, Media, Marketing",
		`what?.txt`:                "what_.txt",
		"trailing. ":               "trailing__",
		"CON.txt":                  "CON_.txt",
		"plain.txt":                "plain.txt",
	}
	for in, want := range cases {
		if got, ok := localNameFor("windows", in); !ok || got != want {
			t.Fatalf("%q -> %q %v, want %q", in, got, ok, want)
		}
	}
	if _, ok := localNameFor("windows", ".."); ok {
		t.Fatal(".. can never be a local name")
	}
	if got, ok := localNameFor("linux", "BS: PR"); !ok || got != "BS: PR" {
		t.Fatal("Linux keeps colons")
	}
}

func TestBuildRemoteSkipsFileWithBadModifiedTime(t *testing.T) {
	all := map[string]*drive.File{
		"bad": {Id: "bad", Name: "x.txt", Parents: []string{"root"}, Md5Checksum: "m", ModifiedTime: "garbage"},
		"ok":  {Id: "ok", Name: "y.txt", Parents: []string{"root"}, Md5Checksum: "m", ModifiedTime: "2026-01-01T00:00:00Z"},
	}
	r, err := buildRemote(all, "root")
	if err != nil {
		t.Fatalf("one bad timestamp aborted the whole listing: %v", err)
	}
	if _, ok := r.Files["x.txt"]; ok {
		t.Fatal("file with unreadable time listed")
	}
	if !r.blocked("x.txt") {
		t.Fatal("skipped file must be blocked so its local copy is not deleted")
	}
	if _, ok := r.Files["y.txt"]; !ok {
		t.Fatal("good file lost")
	}
}
