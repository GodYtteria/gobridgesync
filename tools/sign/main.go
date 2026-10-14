package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"aead.dev/minisign"
)

func main() {
	keygen := flag.String("keygen", "", "write a new encrypted key pair to this directory")
	flag.Parse()
	var err error
	if *keygen != "" {
		err = generate(*keygen)
	} else {
		err = sign(flag.Args())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}

func generate(dir string) error {
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	password := hex.EncodeToString(buf)
	key, err := minisign.EncryptKey(password, priv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"gobridgesync.key": key, "gobridgesync.pass": []byte(password)} {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	text, err := pub.MarshalText()
	if err != nil {
		return err
	}
	fmt.Println(string(text))
	return nil
}

func sign(files []string) error {
	key, password := os.Getenv("GBS_SIGNING_KEY"), os.Getenv("GBS_SIGNING_PASSWORD")
	if key == "" || password == "" || len(files) == 0 {
		return errors.New("usage: GBS_SIGNING_KEY=... GBS_SIGNING_PASSWORD=... go run ./tools/sign FILE...")
	}
	priv, err := minisign.DecryptKey(password, []byte(key))
	if err != nil {
		return err
	}
	for _, f := range files {
		msg, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if err := os.WriteFile(f+".minisig", minisign.Sign(priv, msg), 0o644); err != nil {
			return err
		}
	}
	return nil
}
