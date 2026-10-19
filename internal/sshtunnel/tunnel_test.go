package sshtunnel

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testServer is a minimal in-process SSH server with password and public
// key auth and direct-tcpip forwarding.
type testServer struct {
	host string
	port int
	key  ssh.PublicKey
	ln   net.Listener

	mu       sync.Mutex
	forwards []string
}

func startServer(t *testing.T, user, password string, authorized ssh.PublicKey) *testServer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("denied")
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == user && password != "" && string(pw) == password {
				return nil, nil
			}
			return nil, denied
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == user && authorized != nil && string(k.Marshal()) == string(authorized.Marshal()) {
				return nil, nil
			}
			return nil, denied
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	s := &testServer{host: host, port: p, key: signer.PublicKey(), ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c, cfg)
		}
	}()
	return s
}

func (s *testServer) forwardCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.forwards)
}

func (s *testServer) serve(nc net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "direct-tcpip" {
			nch.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		dest, ok := parseDirectTCPIP(nch.ExtraData())
		if !ok {
			nch.Reject(ssh.ConnectionFailed, "bad payload")
			continue
		}
		s.mu.Lock()
		s.forwards = append(s.forwards, dest)
		s.mu.Unlock()
		target, err := net.Dial("tcp", dest)
		if err != nil {
			nch.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			target.Close()
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() {
			defer ch.Close()
			defer target.Close()
			done := make(chan struct{}, 2)
			go func() { io.Copy(ch, target); done <- struct{}{} }()
			go func() { io.Copy(target, ch); done <- struct{}{} }()
			<-done
		}()
	}
}

// parseDirectTCPIP decodes the RFC 4254 7.2 payload (host, port, ...).
func parseDirectTCPIP(b []byte) (string, bool) {
	if len(b) < 4 {
		return "", false
	}
	n := binary.BigEndian.Uint32(b)
	if uint32(len(b)-4) < n+4 {
		return "", false
	}
	host := string(b[4 : 4+n])
	port := binary.BigEndian.Uint32(b[4+n:])
	return net.JoinHostPort(host, strconv.Itoa(int(port))), true
}

// startEcho starts a line echo server and returns its address.
func startEcho(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					io.WriteString(c, "echo:"+line)
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(t *testing.T, addr, msg string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c, msg+"\n")
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || got != "echo:"+msg+"\n" {
		t.Fatalf("round trip %q %v", got, err)
	}
}

func fixedHost(s *testServer) ssh.HostKeyCallback { return ssh.FixedHostKey(s.key) }

func TestPasswordTunnel(t *testing.T) {
	srv := startServer(t, "alice", "secret", nil)
	echo := startEcho(t)
	tun, err := Open(Config{Host: srv.host, Port: srv.port, User: "alice", Password: "secret", HostKeyCallback: fixedHost(srv)}, echo)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	if !strings.HasPrefix(tun.LocalAddr(), "127.0.0.1:") {
		t.Fatalf("local addr %s", tun.LocalAddr())
	}
	// Several concurrent connections share one SSH session.
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); roundTrip(t, tun.LocalAddr(), "hello"+strconv.Itoa(i)) }()
	}
	wg.Wait()
	if n := srv.forwardCount(); n != 5 {
		t.Fatalf("forwards %d", n)
	}
	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp", tun.LocalAddr(), time.Second); err == nil {
		t.Fatal("listener still open after Close")
	}
	if err := tun.Close(); err != nil {
		t.Fatal("second Close should be a no-op:", err)
	}
}

func writeKey(t *testing.T, passphrase string) (string, ssh.PublicKey) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sp, _ := ssh.NewPublicKey(pub)
	return path, sp
}

func TestKeyTunnel(t *testing.T) {
	echo := startEcho(t)
	t.Run("plain key", func(t *testing.T) {
		path, pub := writeKey(t, "")
		srv := startServer(t, "bob", "", pub)
		tun, err := Open(Config{Host: srv.host, Port: srv.port, User: "bob", KeyPath: path, HostKeyCallback: fixedHost(srv)}, echo)
		if err != nil {
			t.Fatal(err)
		}
		defer tun.Close()
		roundTrip(t, tun.LocalAddr(), "k")
	})
	t.Run("encrypted key", func(t *testing.T) {
		path, pub := writeKey(t, "pp")
		srv := startServer(t, "bob", "", pub)
		_, err := Open(Config{Host: srv.host, Port: srv.port, User: "bob", KeyPath: path, HostKeyCallback: fixedHost(srv)}, echo)
		if err == nil || !strings.Contains(err.Error(), "passphrase") {
			t.Fatalf("expected passphrase error, got %v", err)
		}
		tun, err := Open(Config{Host: srv.host, Port: srv.port, User: "bob", KeyPath: path, KeyPassphrase: "pp", HostKeyCallback: fixedHost(srv)}, echo)
		if err != nil {
			t.Fatal(err)
		}
		defer tun.Close()
		roundTrip(t, tun.LocalAddr(), "k2")
	})
}

func TestTunnelErrors(t *testing.T) {
	srv := startServer(t, "alice", "secret", nil)
	echo := startEcho(t)
	if _, err := Open(Config{Host: srv.host, Port: srv.port, User: "alice", Password: "wrong", HostKeyCallback: fixedHost(srv)}, echo); err == nil {
		t.Error("expected auth failure")
	}
	if _, err := Open(Config{Host: srv.host, Port: srv.port, User: "alice"}, echo); err == nil {
		t.Error("expected error without credentials")
	}
	other := startServer(t, "alice", "secret", nil)
	if _, err := Open(Config{Host: srv.host, Port: srv.port, User: "alice", Password: "secret", HostKeyCallback: fixedHost(other)}, echo); err == nil {
		t.Error("expected host key mismatch")
	}
	if _, err := Open(Config{Host: srv.host, Port: srv.port, User: "alice", KeyPath: "/nonexistent"}, echo); err == nil {
		t.Error("expected missing key error")
	}
	// Unreachable remote: the tunnel opens, but the forwarded connection closes.
	tun, err := Open(Config{Host: srv.host, Port: srv.port, User: "alice", Password: "secret", HostKeyCallback: fixedHost(srv)}, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	c, err := net.Dial("tcp", tun.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Error("expected closed connection for unreachable remote")
	}
	c.Close()
}
