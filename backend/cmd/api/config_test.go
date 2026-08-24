package main

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// testDSN carries a recognisable password so the redaction tests can assert on
// its absence.
const testDSN = "postgres://user:hunter2@localhost:5432/activity_library?sslmode=disable"

// envKeys lists every variable loadConfig reads, plus the unprefixed DSN that
// it must ignore. setEnv clears all of them so a test never inherits a value
// from the developer's shell or from a sibling test.
var envKeys = []string{"ADDR", "DB_DSN", "DSN"}

// setEnv installs exactly the given variables and removes the rest.
//
// t.Setenv records the original value and restores it during cleanup, including
// unsetting variables that were not present to begin with. Calling it before
// os.Unsetenv therefore registers that restore while still letting the test see
// a genuinely absent variable, which is what the required check keys off.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()

	for _, key := range envKeys {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	for key, value := range vars {
		t.Setenv(key, value)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	setEnv(t, map[string]string{"DB_DSN": testDSN})

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned an unexpected error: %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, ":8080")
	}
	if cfg.DB.DSN != testDSN {
		t.Errorf("DB.DSN = %q, want %q", cfg.DB.DSN, testDSN)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	setEnv(t, map[string]string{
		"ADDR":   ":9999",
		"DB_DSN": testDSN,
	})

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned an unexpected error: %v", err)
	}

	if cfg.Addr != ":9999" {
		t.Errorf("Addr = %q, want %q — the environment must win over envDefault", cfg.Addr, ":9999")
	}
}

// TestLoadConfigRequiresDSN is the test that matters most: a constraint that
// never rejects anything is indistinguishable from no constraint at all.
func TestLoadConfigRequiresDSN(t *testing.T) {
	setEnv(t, nil)

	_, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig() succeeded with DB_DSN unset, want an error")
	}
	if !strings.Contains(err.Error(), "DB_DSN") {
		t.Errorf("error %q does not name DB_DSN, so it may be reporting a different problem", err)
	}
}

// TestLoadConfigIgnoresUnprefixedDSN guards the envPrefix tag. An earlier
// version of the library ignored both the prefix and the nested struct
// entirely, which left the DSN empty while reporting no error at all.
func TestLoadConfigIgnoresUnprefixedDSN(t *testing.T) {
	setEnv(t, map[string]string{"DSN": testDSN})

	cfg, err := loadConfig()
	if err == nil {
		t.Fatalf("loadConfig() read the DSN from DSN rather than DB_DSN, giving %q", cfg.DB.DSN)
	}
}

func TestDBConfigLogValueDoesNotLeakDSN(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	logger.Info("config loaded", "db", dbConfig{DSN: testDSN})

	output := buf.String()
	if strings.Contains(output, "hunter2") {
		t.Errorf("log output leaked the DSN password: %s", output)
	}
	if strings.Contains(output, "localhost") {
		t.Errorf("log output leaked the DSN host: %s", output)
	}
	if !strings.Contains(output, "[redacted]") {
		t.Errorf("log output = %s, want it to contain [redacted]", output)
	}
}

func TestDBConfigLogValueReportsUnset(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	logger.Info("config loaded", "db", dbConfig{})

	if output := buf.String(); !strings.Contains(output, "unset") {
		t.Errorf("log output = %s, want it to contain unset", output)
	}
}
