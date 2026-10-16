package main

import (
	"os/exec"
	"runtime"
	"strings"
)

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}

func openPath(p string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer", p).Start()
	case "darwin":
		return exec.Command("open", p).Start()
	}
	return exec.Command("xdg-open", p).Start()
}

func caseInsensitiveFS(goos string) bool {
	return goos == "windows" || goos == "darwin"
}

var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

func localNameFor(goos, name string) (string, bool) {
	if validNameFor(goos, name) {
		return name, true
	}
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, tmpPrefix) {
		return "", false
	}
	bad := "/\x00"
	if goos == "windows" {
		bad += `<>:"\|?*`
	}
	out := []rune(name)
	for i, r := range out {
		if strings.ContainsRune(bad, r) || (goos == "windows" && r < 32) {
			out[i] = '_'
		}
	}
	if goos == "windows" {
		for i := len(out) - 1; i >= 0 && (out[i] == '.' || out[i] == ' '); i-- {
			out[i] = '_'
		}
	}
	s := string(out)
	if !validNameFor(goos, s) {
		base, ext, _ := strings.Cut(s, ".")
		if ext != "" {
			ext = "." + ext
		}
		s = base + "_" + ext
	}
	return s, validNameFor(goos, s)
}

func localName(name string) (string, bool) { return localNameFor(runtime.GOOS, name) }

func validNameFor(goos, name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || strings.HasPrefix(name, tmpPrefix) {
		return false
	}
	if goos != "windows" {
		return true
	}
	if strings.ContainsAny(name, `<>:"\|?*`) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	for _, r := range name {
		if r < 32 {
			return false
		}
	}
	base, _, _ := strings.Cut(name, ".")
	return !windowsReserved[strings.ToUpper(base)]
}
