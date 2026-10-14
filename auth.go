package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func oauthConfig(p Paths, scope string) (*oauth2.Config, error) {
	if b, ok := builtInClient(); ok {
		return google.ConfigFromJSON(b, scope)
	}
	b, err := os.ReadFile(p.credentials())
	if err != nil {
		return nil, fmt.Errorf("read OAuth client (download Desktop app credentials to %s): %w", p.credentials(), err)
	}
	return google.ConfigFromJSON(b, scope)
}

func loginScope(full bool) string {
	if full || internalEdition() {
		return drive.DriveScope
	}
	return drive.DriveFileScope
}

func fullAccess(p Paths) bool {
	b, err := os.ReadFile(p.scope())
	return err == nil && string(b) == drive.DriveScope
}

func login(ctx context.Context, p Paths, full bool, returnURL string) error {
	cfg, err := oauthConfig(p, loginScope(full))
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	cfg.RedirectURL = "http://" + ln.Addr().String()

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	state := hex.EncodeToString(buf)
	verifier := oauth2.GenerateVerifier()
	url := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "consent"))

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	doneCh := make(chan error, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, "login failed", http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("authorization denied: %s", e):
			default:
			}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		select {
		case codeCh <- code:
		default:
			http.Error(w, "login already handled", http.StatusConflict)
			return
		}
		var result error
		select {
		case result = <-doneCh:
		case <-time.After(30 * time.Second):
			result = errors.New("timed out")
		}
		switch {
		case result != nil:
			http.Error(w, "Login failed. Return to GoBridgeSync and try again.", http.StatusBadGateway)
		case returnURL != "":
			http.Redirect(w, r, returnURL, http.StatusFound)
		default:
			fmt.Fprintln(w, "Login complete. You can close this tab.")
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Println("Open this URL to log in:\n" + url)
	_ = openBrowser(url)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Minute):
		return errors.New("login timed out")
	}
	err = finishLogin(ctx, p, cfg, code, verifier, full)
	doneCh <- err
	if err != nil {
		return err
	}
	time.Sleep(time.Second)
	fmt.Println("Logged in.")
	return nil
}

func finishLogin(ctx context.Context, p Paths, cfg *oauth2.Config, code, verifier string, full bool) error {
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return err
	}
	if err := saveToken(p, tok); err != nil {
		return err
	}
	if err := os.Remove(p.index()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return writeFileAtomic(p.scope(), []byte(loginScope(full)), 0o600)
}

type savingSource struct {
	mu   sync.Mutex
	base oauth2.TokenSource
	last string
	p    Paths
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	tok, err := s.base.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tok.AccessToken != s.last {
		s.last = tok.AccessToken
		if err := saveToken(s.p, tok); err != nil {
			log.Printf("save token: %v", err)
		}
	}
	return tok, nil
}

func tokenSource(ctx context.Context, p Paths) (oauth2.TokenSource, error) {
	cfg, err := oauthConfig(p, drive.DriveFileScope)
	if err != nil {
		return nil, err
	}
	tok, err := loadToken(p)
	if err != nil {
		return nil, fmt.Errorf("not logged in, run: gobridgesync login (%w)", err)
	}
	return &savingSource{base: cfg.TokenSource(ctx, tok), last: tok.AccessToken, p: p}, nil
}

func newService(ctx context.Context, p Paths) (*drive.Service, error) {
	srv, _, err := connect(ctx, p)
	return srv, err
}

func connect(ctx context.Context, p Paths) (*drive.Service, *http.Client, error) {
	ts, err := tokenSource(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	client := oauth2.NewClient(ctx, ts)
	srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, nil, err
	}
	return srv, client, nil
}
