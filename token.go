package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const keyringService = "GoBridgeSync"

var (
	keyringTimeout    = 10 * time.Second
	errKeyringTimeout = errors.New("system keyring did not respond")
)

func withTimeout[T any](f func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := f()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(keyringTimeout):
		var zero T
		return zero, errKeyringTimeout
	}
}

func keyringSet(user, secret string) error {
	_, err := withTimeout(func() (struct{}, error) { return struct{}{}, keyring.Set(keyringService, user, secret) })
	return err
}

func keyringGet(user string) (string, error) {
	return withTimeout(func() (string, error) { return keyring.Get(keyringService, user) })
}

func keyringDelete(user string) error {
	_, err := withTimeout(func() (struct{}, error) { return struct{}{}, keyring.Delete(keyringService, user) })
	return err
}

func (p Paths) keyringUser() string { return "google-token:" + p.ConfigDir }

func saveToken(p Paths, tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	if err := keyringSet(p.keyringUser(), string(b)); err != nil {
		log.Printf("system keyring unavailable, storing token in %s: %v", p.token(), err)
		return writeFileAtomic(p.token(), b, 0o600)
	}
	if err := os.Remove(p.token()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func loadToken(p Paths) (*oauth2.Token, error) {
	s, kerr := keyringGet(p.keyringUser())
	if kerr == nil {
		tok := &oauth2.Token{}
		return tok, json.Unmarshal([]byte(s), tok)
	}
	b, err := os.ReadFile(p.token())
	if err != nil {
		return nil, err
	}
	tok := &oauth2.Token{}
	if err := json.Unmarshal(b, tok); err != nil {
		return nil, err
	}
	if errors.Is(kerr, keyring.ErrNotFound) {
		if err := saveToken(p, tok); err != nil {
			log.Printf("move token to keyring: %v", err)
		}
	}
	return tok, nil
}

func hasToken(p Paths) bool {
	_, err := loadToken(p)
	return err == nil
}

func deleteToken(p Paths) error {
	err := keyringDelete(p.keyringUser())
	if errors.Is(err, errKeyringTimeout) {
		log.Printf("sign out: %v; the saved Google token may remain in the system keyring", err)
	} else if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	if err := os.Remove(p.token()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func resetAccountState(p Paths) error {
	for _, f := range []string{p.index(), p.state(), p.backupState(), p.sessions(), p.scope(), p.index() + ".log", p.syncIDs(), p.backupIDs()} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
