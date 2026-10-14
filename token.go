package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const keyringService = "GoBridgeSync"

func (p Paths) keyringUser() string { return "google-token:" + p.ConfigDir }

func saveToken(p Paths, tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	if err := keyring.Set(keyringService, p.keyringUser(), string(b)); err != nil {
		log.Printf("system keyring unavailable, storing token in %s: %v", p.token(), err)
		return writeFileAtomic(p.token(), b, 0o600)
	}
	if err := os.Remove(p.token()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func loadToken(p Paths) (*oauth2.Token, error) {
	s, kerr := keyring.Get(keyringService, p.keyringUser())
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
	err := keyring.Delete(keyringService, p.keyringUser())
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	if err := os.Remove(p.token()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func resetAccountState(p Paths) error {
	for _, f := range []string{p.index(), p.state(), p.backupState(), p.sessions(), p.scope()} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
