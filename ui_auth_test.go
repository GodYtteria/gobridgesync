package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStaleTokenGetsAJSONReloadHint(t *testing.T) {
	u := &ui{addr: "127.0.0.1:1", token: "new"}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:1/api/dirs", nil)
	r.Header.Set("X-GBS-Token", "old")
	if u.authorized(w, r) {
		t.Fatal("an old token must be refused")
	}
	var body struct {
		Error  string `json:"error"`
		Reload bool   `json:"reload"`
	}
	if w.Code != http.StatusForbidden || json.Unmarshal(w.Body.Bytes(), &body) != nil || !body.Reload || body.Error == "" {
		t.Fatalf("the page must get JSON it can read and a reload hint, got %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:1/api/dirs", nil)
	r.Header.Set("X-GBS-Token", "new")
	r.Header.Set("Origin", "http://evil.example")
	if u.authorized(w, r) || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Reload {
		t.Fatalf("a foreign origin must be refused without a reload hint: %q", w.Body.String())
	}
}
