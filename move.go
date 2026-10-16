package main

import (
	"io"
	"os"
)

var renameFile = os.Rename

func moveFile(src, dst string) error {
	err := renameFile(src, dst)
	if err == nil || !isCrossDevice(err) {
		return err
	}
	return copyThenRemove(src, dst)
}

func copyThenRemove(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chtimes(dst, info.ModTime(), info.ModTime())
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}
