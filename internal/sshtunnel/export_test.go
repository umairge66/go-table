package sshtunnel

import (
	"testing"

	"golang.org/x/crypto/ssh"
)

// StartTestServer exposes the in-process SSH server to external tests.
func StartTestServer(t *testing.T, user, password string, authorized ssh.PublicKey) (host string, port int, forwards func() int) {
	s := startServer(t, user, password, authorized)
	return s.host, s.port, s.forwardCount
}
