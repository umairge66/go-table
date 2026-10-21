// Package sshtunnel forwards a local TCP port to a remote address through
// an SSH server.
package sshtunnel

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Config describes the SSH server and credentials.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	// KeyPath is a private key file; KeyPassphrase decrypts it.
	KeyPath       string
	KeyPassphrase string
	// HostKeyCallback verifies the server. Nil uses ~/.ssh/known_hosts when
	// it exists and otherwise accepts any key (trust on first use is left to
	// the caller).
	HostKeyCallback ssh.HostKeyCallback
	Timeout         time.Duration
	KeepAlive       time.Duration
}

// Tunnel is a running local port forward.
type Tunnel struct {
	client   *ssh.Client
	listener net.Listener
	remote   string
	wg       sync.WaitGroup
	once     sync.Once
	done     chan struct{}
}

// Open connects to the SSH server and starts forwarding a random local port
// on 127.0.0.1 to remote (host:port as seen from the SSH server).
func Open(cfg Config, remote string) (*Tunnel, error) {
	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	hk := cfg.HostKeyCallback
	if hk == nil {
		hk = defaultHostKeyCallback()
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(cfg.Host, fmt.Sprint(port)), &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: hk,
		Timeout:         timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("ssh: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		client.Close()
		return nil, err
	}
	t := &Tunnel{client: client, listener: ln, remote: remote, done: make(chan struct{})}
	t.wg.Add(1)
	go t.accept()
	ka := cfg.KeepAlive
	if ka == 0 {
		ka = 30 * time.Second
	}
	go t.keepAlive(ka)
	return t, nil
}

// LocalAddr is the 127.0.0.1:port address to connect the database to.
func (t *Tunnel) LocalAddr() string { return t.listener.Addr().String() }

// Close stops forwarding and disconnects from the SSH server.
func (t *Tunnel) Close() error {
	var err error
	t.once.Do(func() {
		close(t.done)
		err = errors.Join(t.listener.Close(), t.client.Close())
		t.wg.Wait()
	})
	return err
}

func (t *Tunnel) accept() {
	defer t.wg.Done()
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return
		}
		t.wg.Add(1)
		go t.forward(local)
	}
}

func (t *Tunnel) forward(local net.Conn) {
	defer t.wg.Done()
	defer local.Close()
	remote, err := t.client.Dial("tcp", t.remote)
	if err != nil {
		return
	}
	defer remote.Close()
	// Close both sides as soon as either direction ends or the tunnel stops.
	stop := make(chan struct{}, 2)
	pipe := func(dst io.Writer, src io.Reader) {
		io.Copy(dst, src)
		stop <- struct{}{}
	}
	go pipe(remote, local)
	go pipe(local, remote)
	select {
	case <-stop:
	case <-t.done:
	}
}

func (t *Tunnel) keepAlive(every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
			if _, _, err := t.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				t.Close()
				return
			}
		}
	}
}

func authMethods(cfg Config) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if cfg.KeyPath != "" {
		path := expandHome(cfg.KeyPath)
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read SSH key: %w", err)
		}
		var signer ssh.Signer
		if cfg.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(cfg.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, fmt.Errorf("SSH key %s is encrypted: enter its passphrase", path)
		}
		if err != nil {
			return nil, fmt.Errorf("parse SSH key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		pw := cfg.Password
		methods = append(methods, ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = pw
				}
				return answers, nil
			}))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("SSH needs a password or a private key")
	}
	return methods, nil
}

// CheckKey reports whether path holds a private key that can sign in, and
// whether it needs a passphrase first. The error says what is wrong in
// words meant for the connection form.
func CheckKey(path string) (encrypted bool, err error) {
	path = expandHome(path)
	st, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("no file at %s", path)
	case err != nil:
		return false, fmt.Errorf("cannot read %s: %w", path, err)
	case st.IsDir():
		return false, fmt.Errorf("%s is a folder, not a key file", path)
	case st.Size() > 1<<20:
		return false, fmt.Errorf("%s is too large to be an SSH key", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("cannot read %s: %w", path, err)
	}
	text := strings.TrimSpace(string(data))
	switch {
	case strings.HasPrefix(text, "PuTTY-User-Key-File"):
		return false, errors.New("this is a PuTTY key (.ppk): export it in OpenSSH format with PuTTYgen")
	case strings.HasPrefix(text, "ssh-") || strings.HasPrefix(text, "ecdsa-") || strings.HasSuffix(path, ".pub"):
		return false, errors.New("this is a public key: choose the file without .pub")
	}
	_, err = ssh.ParsePrivateKey(data)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return true, nil
	}
	if err != nil {
		return false, errors.New("this file is not an SSH private key")
	}
	return false, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func defaultHostKeyCallback() ssh.HostKeyCallback {
	home, err := os.UserHomeDir()
	if err == nil {
		path := filepath.Join(home, ".ssh", "known_hosts")
		if cb, err := knownhosts.New(path); err == nil {
			return func(host string, remote net.Addr, key ssh.PublicKey) error {
				err := cb(host, remote, key)
				var ke *knownhosts.KeyError
				// Unknown hosts are accepted; changed keys are rejected.
				if errors.As(err, &ke) && len(ke.Want) == 0 {
					return nil
				}
				return err
			}
		}
	}
	return ssh.InsecureIgnoreHostKey()
}
