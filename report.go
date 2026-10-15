package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

var reportURL = ""

const (
	reportLogLines = 50
	reportEvery    = 24 * time.Hour
	reportTimeout  = 15 * time.Second
)

var (
	digits    = regexp.MustCompile(`\d+`)
	dateLike  = regexp.MustCompile(`^[\d/:.-]+$`)
	pathLike  = regexp.MustCompile(`[^\s"'()=]*[/\\][^\s"'()=]+`)
	secretish = regexp.MustCompile(`(?i)(ya29\.[\w-]+|1//[\w-]{20,}|AIza[\w-]{30,}|ghp_\w{20,}|github_pat_\w{20,}|(password|secret|token)\s*[=:]\s*\S+)`)
)

func redact(s string) string {
	s = secretish.ReplaceAllString(s, "[redacted]")
	return pathLike.ReplaceAllStringFunc(s, func(p string) string {
		if dateLike.MatchString(p) || strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
			return p
		}
		return "…/" + path.Base(strings.ReplaceAll(p, `\`, "/"))
	})
}

func tailLines(file string, n int) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return lines
}

type reporter struct {
	mu   sync.Mutex
	sent map[string]time.Time
	url  string
	log  string
	post func(context.Context, string, []byte) error
}

func newReporter(logFile string) *reporter {
	return &reporter{sent: map[string]time.Time{}, url: reportURL, log: logFile, post: postJSON}
}

func postJSON(ctx context.Context, url string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("report collector: %s", resp.Status)
	}
	return nil
}

func normalizeProblem(msg string) string {
	return digits.ReplaceAllString(redact(msg), "#")
}

func (r *reporter) send(ctx context.Context, problems []string, now time.Time) int {
	if r == nil || r.url == "" {
		return 0
	}
	sent := 0
	for _, p := range problems {
		key := normalizeProblem(p)
		r.mu.Lock()
		last, seen := r.sent[key]
		if seen && now.Sub(last) < reportEvery {
			r.mu.Unlock()
			continue
		}
		r.sent[key] = now
		r.mu.Unlock()
		lines := tailLines(r.log, reportLogLines)
		for i := range lines {
			lines[i] = redact(lines[i])
		}
		body, err := json.Marshal(map[string]string{
			"app":     releaseName(),
			"version": version,
			"os":      runtime.GOOS + "/" + runtime.GOARCH,
			"error":   redact(p),
			"log":     strings.Join(lines, "\n"),
		})
		if err != nil {
			continue
		}
		if err := r.post(ctx, r.url, body); err != nil {
			r.mu.Lock()
			delete(r.sent, key)
			r.mu.Unlock()
			continue
		}
		sent++
	}
	return sent
}
