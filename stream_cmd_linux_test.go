//go:build linux

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunStreamRefusesARelativeMountFolder(t *testing.T) {
	err := runStream(context.Background(), Paths{StateDir: t.TempDir()}, "GoogleDrive-Stream", 1, nil)
	if err == nil {
		t.Fatal("a relative mount folder (no home folder) must be refused, not mounted under the working directory")
	}
}

func TestMountFSTypeOnlyMatchesTheExactMountpoint(t *testing.T) {
	info := `36 25 0:30 / /home/u/GoogleDrive-Stream rw,nosuid,nodev,relatime shared:1 - fuse.gobridgesync gobridgesync rw,user_id=1000
37 25 0:31 / /home/u/other\040mount rw,relatime - fuse.sshfs host:/x rw
38 25 0:32 / /home/u/GoogleDrive-Stream2 rw - ext4 /dev/sda1 rw`
	if fs, ok := mountFSType(info, "/home/u/GoogleDrive-Stream"); !ok || fs != "fuse.gobridgesync" {
		t.Fatalf("got %q %v", fs, ok)
	}
	if fs, ok := mountFSType(info, "/home/u/other mount"); !ok || fs != "fuse.sshfs" {
		t.Fatalf("escaped space not handled: %q %v", fs, ok)
	}
	if _, ok := mountFSType(info, "/home/u/GoogleDrive"); ok {
		t.Fatal("a prefix of another mountpoint must not match")
	}
	if _, ok := mountFSType("", "/x"); ok {
		t.Fatal("no mounts, no match")
	}
}

func swapFusermount(t *testing.T, run func(tool, dir string) ([]byte, error)) {
	prev := runFusermount
	runFusermount = run
	t.Cleanup(func() { runFusermount = prev })
}

func TestLazyUnmountTriesFusermountThenFallsBackAndReportsFailure(t *testing.T) {
	var tools []string
	swapFusermount(t, func(tool, dir string) ([]byte, error) {
		tools = append(tools, tool)
		if tool == "fusermount3" {
			return nil, errors.New("not found")
		}
		return nil, nil
	})
	if err := lazyUnmount("/x"); err != nil || len(tools) != 2 || tools[1] != "fusermount" {
		t.Fatalf("err=%v tools=%v", err, tools)
	}
	swapFusermount(t, func(tool, dir string) ([]byte, error) { return []byte("busy"), errors.New("exit 1") })
	if err := lazyUnmount("/x"); err == nil {
		t.Fatal("a failed lazy unmount must be reported")
	}
}

func TestStaleMountThatCannotBeReleasedStopsTheMount(t *testing.T) {
	swapFusermount(t, func(tool, dir string) ([]byte, error) { return nil, errors.New("exit 1") })
	info := "36 25 0:30 / /x rw - fuse.gobridgesync gobridgesync rw"
	if err := releaseStaleMount(info, "/x"); err == nil {
		t.Fatal("stacking a second mount on an unreleased one must be refused")
	}
	if err := releaseStaleMount(info, "/y"); err != nil {
		t.Fatalf("a folder that is not mounted needs nothing: %v", err)
	}
	if err := releaseStaleMount("36 25 0:30 / /x rw - ext4 /dev/sda1 rw", "/x"); err == nil {
		t.Fatal("a foreign mount must be refused")
	}
}

func TestReleaseMountWaitsForTheUnmountInsteadOfAbandoningIt(t *testing.T) {
	prev := streamUnmountWait
	streamUnmountWait = 10 * time.Millisecond
	t.Cleanup(func() { streamUnmountWait = prev })
	var finished atomic.Bool
	releaseMount(func() error {
		time.Sleep(80 * time.Millisecond)
		finished.Store(true)
		return nil
	}, "/x")
	if !finished.Load() {
		t.Fatal("the mount code returned while the unmount was still running; it could unmount the next mount")
	}
}

func TestUnmountStreamCallsUnmountOnceAndFallsBackLazily(t *testing.T) {
	var lazy int
	swapFusermount(t, func(tool, dir string) ([]byte, error) { lazy++; return nil, nil })
	var calls int
	unmountStream(func() error { calls++; return nil }, "/x")
	if calls != 1 || lazy != 0 {
		t.Fatalf("calls=%d lazy=%d", calls, lazy)
	}
	unmountStream(func() error { calls++; return errors.New("busy") }, "/x")
	if calls != 2 || lazy != 1 {
		t.Fatalf("calls=%d lazy=%d", calls, lazy)
	}
}
