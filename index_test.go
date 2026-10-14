package main

import (
	"testing"

	"google.golang.org/api/drive/v3"
)

func folder(id, name, parent string) *drive.File {
	return &drive.File{Id: id, Name: name, MimeType: folderMime, Parents: []string{parent}}
}

func file(id, name, parent, sum string) *drive.File {
	return &drive.File{Id: id, Name: name, MimeType: "text/plain", Parents: []string{parent}, Md5Checksum: sum, ModifiedTime: "2026-01-01T00:00:00Z"}
}

func TestBuildRemoteWithRoot(t *testing.T) {
	all := map[string]*drive.File{
		"f1": folder("f1", "Docs", "ROOT"),
		"a":  file("a", "a.txt", "f1", "m1"),
		"d1": file("d1", "dup.txt", "f1", "x"),
		"d2": file("d2", "dup.txt", "f1", "y"),
		"g":  {Id: "g", Name: "Notes", MimeType: gappsPrefix + "document", Parents: []string{"f1"}, WebViewLink: "u"},
		"o":  file("o", "orphan.txt", "missing", "z"),
	}
	r, err := buildRemote(all, "ROOT")
	if err != nil {
		t.Fatal(err)
	}
	if r.Files["Docs/a.txt"].ID != "a" || r.Folders["Docs"] != "f1" || r.Docs["Docs/Notes.gdoc"].ID != "g" {
		t.Fatalf("remote %+v", r)
	}
	if _, ok := r.Files["Docs/dup.txt"]; ok || !r.blocked("Docs/dup.txt") {
		t.Fatal("duplicates must be blocked")
	}
	if len(r.Files) != 1 {
		t.Fatalf("orphan included: %+v", r.Files)
	}
}

func TestBuildRemoteAppOnlyAccess(t *testing.T) {
	all := map[string]*drive.File{
		"b":  folder("b", backupRootName, "HIDDEN"),
		"s":  folder("s", syncRootName, "HIDDEN"),
		"x":  folder("x", "Other", "HIDDEN"),
		"f":  file("f", "a.txt", "s", "m"),
		"f2": file("f2", "b.txt", "x", "m"),
	}
	r, err := buildRemote(all, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Files[syncRootName+"/a.txt"].ID != "f" || r.Folders[backupRootName] != "b" || len(r.Files) != 1 {
		t.Fatalf("remote %+v", r)
	}
}

func TestApplyChanges(t *testing.T) {
	files := map[string]*drive.File{"a": file("a", "a.txt", "p", "1"), "b": file("b", "b.txt", "p", "1"), "c": file("c", "c.txt", "p", "1")}
	applyChanges(files, []*drive.Change{
		{FileId: "a", Removed: true},
		{FileId: "b", File: &drive.File{Id: "b", Trashed: true}},
		{FileId: "c", File: file("c", "c2.txt", "p", "2")},
		{FileId: "d", File: file("d", "d.txt", "p", "3")},
		{FileId: ""},
	})
	if len(files) != 2 || files["c"].Name != "c2.txt" || files["d"] == nil {
		t.Fatalf("files %+v", files)
	}
}
