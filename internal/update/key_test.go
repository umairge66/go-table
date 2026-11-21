package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicKey(t *testing.T) {
	k, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(k) != ed25519.PublicKeySize {
		t.Fatalf("PublicKey isn't an Ed25519 key: %v", err)
	}
	// GOTABLE_SIGNED_DIR: a folder release-sign signed, checked with the
	// key built into GoTable (as the release pipeline's output would be).
	dir := os.Getenv("GOTABLE_SIGNED_DIR")
	if dir == "" {
		t.Skip("GOTABLE_SIGNED_DIR not set")
	}
	sums, err1 := os.ReadFile(filepath.Join(dir, SumsName))
	sig, err2 := os.ReadFile(filepath.Join(dir, SigName))
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if err := Verify(ed25519.PublicKey(k), sums, sig); err != nil {
		t.Fatal(err)
	}
	if len(ParseSums(sums)) == 0 {
		t.Fatal("no checksums")
	}
}
