// Command release-sign vouches for a release's files: it writes SHA256SUMS
// for every file in a folder and signs it (SHA256SUMS.sig, Ed25519), which
// GoTable checks before installing an update (internal/update).
//
//	release-sign dist        sign dist/*, with the key in GOTABLE_RELEASE_KEY
//	release-sign -genkey F   make a key pair: the private key goes to file F
//	                         (keep it, and store it as the GOTABLE_RELEASE_KEY
//	                         secret); the public key is printed, for
//	                         internal/update/key.go
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gotable/internal/update"
)

func main() {
	genkey := flag.String("genkey", "", "write a new private key to this file and print the public key")
	flag.Parse()
	if *genkey != "" {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fail(err)
		}
		// O_EXCL: never overwrite a key that may already sign releases.
		f, err := os.OpenFile(*genkey, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			fail(err)
		}
		fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv.Seed()))
		if err := f.Close(); err != nil {
			fail(err)
		}
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return
	}
	dir := flag.Arg(0)
	if dir == "" {
		fail(fmt.Errorf("usage: release-sign <folder> | -genkey <file>"))
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("GOTABLE_RELEASE_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		fail(fmt.Errorf("GOTABLE_RELEASE_KEY is missing or isn't a key from -genkey: a release must be signed, or GoTable won't install it"))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	// The key must be the one GoTable trusts, or every update would be refused.
	if pub, err := base64.StdEncoding.DecodeString(update.PublicKey); err != nil || !priv.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(pub)) {
		fail(fmt.Errorf("GOTABLE_RELEASE_KEY doesn't match the public key in internal/update/key.go"))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fail(err)
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && n != update.SumsName && n != update.SigName {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		fail(fmt.Errorf("%s has no files to sign", dir))
	}
	sums, err := update.Sums(dir, names)
	if err != nil {
		fail(err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, update.SumsName), sums, 0o644); err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, update.SigName), []byte(sig), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("signed %d files:\n%s", len(names), sums)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "release-sign:", err)
	os.Exit(1)
}
