package store

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// These tests need no database. Every case here is one InitPool rejects before
// or during the ping, which is exactly where the error text is most likely to
// quote the connection string back at the caller.

// testPassword is embedded in the DSN each test builds so an assertion can look
// for it by name. It must never reach an error string: main logs InitPool's
// error, so a leak here is a password in the logs.
const testPassword = "hunter2"

// closedPort reserves a port, releases it, and returns it. Connecting to the
// result is refused immediately, which keeps the unreachable-database test fast
// and independent of any real host.
func closedPort(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not reserve a port: %v", err)
	}

	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("could not release the reserved port %s: %v", addr, err)
	}

	return addr
}

// assertNoPasswordLeak is the assertion the whole file exists for.
func assertNoPasswordLeak(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("InitPool() succeeded, want an error")
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("error leaked the DSN password: %v", err)
	}
}

// TestInitPoolRejectsInvalidDSN guards the deliberate choice not to wrap the
// ParseConfig error: pgx quotes the whole connection string in it, password
// included. Replacing the flat message with %w would reintroduce the leak, and
// nothing else in the build would object.
func TestInitPoolRejectsInvalidDSN(t *testing.T) {
	dsn := "::not-a-dsn::" + testPassword

	pool, err := InitPool(context.Background(), PoolConfig{
		DSN:      dsn,
		MaxConns: 25,
		MinConns: 5,
	})
	if pool != nil {
		pool.Close()
		t.Error("InitPool() returned a pool alongside an error, want nil")
	}

	assertNoPasswordLeak(t, err)

	if strings.Contains(err.Error(), dsn) {
		t.Errorf("error quoted the DSN back: %v", err)
	}
}

// TestInitPoolFailsWhenDatabaseUnreachable covers the ping. pgx redacts the
// password in connect errors, but the test pins that behaviour rather than
// trusting it, since the error reaches a log either way.
func TestInitPoolFailsWhenDatabaseUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := InitPool(ctx, PoolConfig{
		DSN:      "postgres://user:" + testPassword + "@" + closedPort(t) + "/activity_library?sslmode=disable",
		MaxConns: 25,
		MinConns: 5,
	})
	if pool != nil {
		pool.Close()
		t.Error("InitPool() returned a pool for an unreachable database, want nil")
	}

	assertNoPasswordLeak(t, err)
}

// TestInitPoolRejectsZeroMaxConns documents what happens when MaxConns is left
// at its zero value: pgxpool refuses the config outright. The envDefault tag on
// dbConfig is what normally prevents this, so a failure here means a config
// change removed the only thing standing between a fresh checkout and a pool
// that cannot be built.
func TestInitPoolRejectsZeroMaxConns(t *testing.T) {
	pool, err := InitPool(context.Background(), PoolConfig{
		DSN: "postgres://user:" + testPassword + "@" + closedPort(t) + "/activity_library?sslmode=disable",
	})
	if pool != nil {
		pool.Close()
		t.Error("InitPool() built a pool with MaxConns unset, want an error")
	}

	assertNoPasswordLeak(t, err)
}
