package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

const logLimit = 5 << 20

func (p Paths) logFile() string { return filepath.Join(p.StateDir, appID()+".log") }

type rotatingLog struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	size     int64
	failedAt time.Time
}

func (l *rotatingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil || l.size+int64(len(b)) > logLimit {
		if l.f != nil {
			l.f.Close()
			l.f = nil
			os.Rename(l.path, l.path+".1")
		}
		if time.Since(l.failedAt) < time.Minute {
			return len(b), nil
		}
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			l.f, l.failedAt = nil, time.Now()
			return len(b), nil
		}
		info, _ := f.Stat()
		l.f, l.size = f, info.Size()
	}
	n, err := l.f.Write(b)
	l.size += int64(n)
	return n, err
}

func startLogFile(p Paths) {
	if err := os.MkdirAll(p.StateDir, 0o700); err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, &rotatingLog{path: p.logFile()}))
	if f, err := os.OpenFile(p.logFile()+".crash", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		debug.SetCrashOutput(f, debug.CrashOptions{})
		f.Close()
	}
	log.Printf("%s %s starting", releaseName(), version)
}
