package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStagingUpdaterPicksTheNewestReleaseIncludingPreReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"tag_name":"v0.3.0-rc16","draft":false},{"tag_name":"v0.3.0-rc17","draft":true},{"tag_name":"v0.2.4","draft":false},{"tag_name":"junk"}]`))
	}))
	t.Cleanup(srv.Close)
	u := &updater{client: srv.Client(), api: srv.URL, list: true, current: "v0.3.0-rc15"}
	tag, err := u.latest(context.Background())
	if err != nil || tag != "v0.3.0-rc16" {
		t.Fatalf("staging must follow the newest published release, skipping drafts: %q %v", tag, err)
	}
	if !newerVersion("v0.3.0", "v0.3.0-rc16") {
		t.Fatal("a stable release must replace the release candidate it came from")
	}
}

func TestStagingUpdaterRejectsAnEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) }))
	t.Cleanup(srv.Close)
	u := &updater{client: srv.Client(), api: srv.URL, list: true, current: "v0.3.0-rc15"}
	if _, err := u.latest(context.Background()); err == nil {
		t.Fatal("no releases must be an error, not an update to nothing")
	}
}
