package server

import (
	"context"
	"errors"
	"strings"
)

// Friendly is an error explained in plain language with a hint.
type Friendly struct {
	Title, Hint string
}

// Explain maps common driver and network errors to plain language.
func Explain(err error) Friendly {
	msg := strings.ToLower(err.Error())
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(msg, s) {
				return true
			}
		}
		return false
	}
	switch {
	// Specific messages first: they contain words the general rules below match.
	case has("other objects depend on it", "referenced by a foreign key", "cannot drop table") ||
		has("foreign key constraint failed") && has("drop"):
		return Friendly{"Other tables or views depend on it", "Turn on “Also drop dependent objects”, or delete the dependent tables too."}
	case has("duplicate column", "already exists"):
		return Friendly{"That name is already taken", "Choose a different column or table name."}
	case has("doesn't look like a column type", "default expression", "sqlite only allows", "sqlite can only add", "can't default to null", "column position"):
		return Friendly{"Please check the column", ""}
	case errors.Is(err, context.Canceled) || has("canceling statement due to user request", "query execution was interrupted"):
		return Friendly{"Query stopped", "You cancelled it before it finished."}
	case errors.Is(err, context.DeadlineExceeded) || has("i/o timeout", "timeout expired"):
		return Friendly{"The server took too long to answer", "Check the host and port, VPN or firewall, and that the server is running."}
	case has("connection refused"):
		return Friendly{"Can't reach the server", "Nothing is listening on that host and port. Is the database running, and is the port right?"}
	case has("no such host", "server misbehaving"):
		return Friendly{"Unknown host name", "The host name could not be resolved. Check for typos, or try the IP address."}
	case has("password authentication failed", "access denied", "authentication failed", "unable to authenticate"):
		if has("ssh") {
			return Friendly{"SSH login failed", "The SSH user, password or private key was not accepted."}
		}
		return Friendly{"Wrong user name or password", "The server rejected the credentials. Re-enter the password in the connection settings."}
	case has("passphrase"):
		return Friendly{"Private key is locked", "Enter the passphrase of the SSH private key."}
	case has("knownhosts", "host key mismatch"):
		return Friendly{"SSH host key changed", "The server's key does not match ~/.ssh/known_hosts. The server may have been reinstalled, or someone may be intercepting the connection."}
	case has("does not exist") && has("database"), has("unknown database"):
		return Friendly{"Database not found", "The database name is wrong, or your user cannot see it. Leave it empty to use the default."}
	case has("tls", "ssl", "certificate", "x509"):
		return Friendly{"Secure connection (SSL) failed", "Try another SSL mode, or check the CA and client certificate files."}
	case has("no such table", "doesn't exist", "unknown table") || has("relation") && has("does not exist"):
		return Friendly{"Table not found", "It may have been renamed or dropped. Refresh the sidebar."}
	case has("syntax error", "you have an error in your sql syntax"):
		return Friendly{"SQL syntax error", "Check the statement near the position mentioned below."}
	case has("duplicate", "unique constraint", "unique violation"):
		return Friendly{"Duplicate value", "A unique column or primary key already has this value."}
	case has("foreign key"):
		return Friendly{"Blocked by a foreign key", "Other rows reference this row, or the referenced row does not exist."}
	case has("not null", "cannot be null", "not-null"):
		return Friendly{"A required column is empty", "Fill in every column marked NOT NULL, or give it a default."}
	case has("invalid input syntax", "incorrect integer value", "incorrect decimal", "datatype mismatch"):
		return Friendly{"Value doesn't fit the column type", "For example, text in a number column or a malformed date."}
	case has("no longer exists or was changed"):
		return Friendly{"The row changed in the meantime", "Someone else edited or deleted it. Refresh to see the current data; nothing was saved."}
	case has("connection is closed", "bad connection", "broken pipe", "connection reset", "was closed"):
		return Friendly{"The connection was lost", "Reconnect to continue."}
	case has("required", "out of range", "unsupported"):
		return Friendly{"Please check the form", ""}
	}
	return Friendly{"Something went wrong", ""}
}
