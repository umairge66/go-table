package model

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// SecretKind distinguishes the secrets stored per connection.
type SecretKind string

const (
	SecretDBPassword    SecretKind = "db_password"
	SecretSSHPassword   SecretKind = "ssh_password"
	SecretSSHPassphrase SecretKind = "ssh_key_passphrase"
)

const keyringService = "gotable"

// Secrets stores passwords in the desktop keyring (Secret Service on Linux).
type Secrets interface {
	Get(connID string, kind SecretKind) (string, error)
	Set(connID string, kind SecretKind, value string) error
	Clear(connID string) error
}

// KeyringSecrets is the production Secrets implementation.
type KeyringSecrets struct{}

func secretKey(connID string, kind SecretKind) string { return connID + ":" + string(kind) }

// Get returns the secret or "" when none is stored.
func (KeyringSecrets) Get(connID string, kind SecretKind) (string, error) {
	v, err := keyring.Get(keyringService, secretKey(connID, kind))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return v, err
}

// Set stores the secret; an empty value deletes it.
func (k KeyringSecrets) Set(connID string, kind SecretKind, value string) error {
	if value == "" {
		err := keyring.Delete(keyringService, secretKey(connID, kind))
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}
	return keyring.Set(keyringService, secretKey(connID, kind), value)
}

// Clear removes every secret of a connection.
func (k KeyringSecrets) Clear(connID string) error {
	var errs []error
	for _, kind := range []SecretKind{SecretDBPassword, SecretSSHPassword, SecretSSHPassphrase} {
		errs = append(errs, k.Set(connID, kind, ""))
	}
	return errors.Join(errs...)
}

// MemorySecrets keeps secrets in memory; used in tests and when no keyring
// is available.
type MemorySecrets struct{ m map[string]string }

// NewMemorySecrets returns an empty in-memory secret store.
func NewMemorySecrets() *MemorySecrets { return &MemorySecrets{m: map[string]string{}} }

func (s *MemorySecrets) Get(connID string, kind SecretKind) (string, error) {
	return s.m[secretKey(connID, kind)], nil
}

func (s *MemorySecrets) Set(connID string, kind SecretKind, value string) error {
	if value == "" {
		delete(s.m, secretKey(connID, kind))
	} else {
		s.m[secretKey(connID, kind)] = value
	}
	return nil
}

func (s *MemorySecrets) Clear(connID string) error {
	for _, kind := range []SecretKind{SecretDBPassword, SecretSSHPassword, SecretSSHPassphrase} {
		delete(s.m, secretKey(connID, kind))
	}
	return nil
}
