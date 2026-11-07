package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAutoAssignShareConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`secrets:
  jwtSecret: "12345678901234567890123456789012"
  credentialEncryptionKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
bootstrapAdmin:
  password: "password123"
routing:
  autoAssignMaxNodeShare: 0.3
  autoAssignMaxMigrationShare: 0.2
`), 0o600); err != nil {
		t.Fatal(err)
	}
	// The overrides must not depend on the database URL override being set.
	t.Setenv(DatabaseURLEnv, "")
	return path
}

func TestLoadAutoAssignShareEnvironmentOverridesYAML(t *testing.T) {
	path := writeAutoAssignShareConfig(t)
	t.Setenv(AutoAssignMaxNodeShareEnv, " 0.25 ")
	t.Setenv(AutoAssignMaxMigrationShareEnv, "0")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routing.AutoAssignMaxNodeShare != 0.25 || cfg.Routing.AutoAssignMaxMigrationShare != 0 {
		t.Fatalf("auto-assign shares = %v/%v, want env 0.25/0", cfg.Routing.AutoAssignMaxNodeShare, cfg.Routing.AutoAssignMaxMigrationShare)
	}
}

func TestLoadEmptyAutoAssignShareEnvironmentKeepsYAML(t *testing.T) {
	path := writeAutoAssignShareConfig(t)
	t.Setenv(AutoAssignMaxNodeShareEnv, "")
	t.Setenv(AutoAssignMaxMigrationShareEnv, "   ")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routing.AutoAssignMaxNodeShare != 0.3 || cfg.Routing.AutoAssignMaxMigrationShare != 0.2 {
		t.Fatalf("auto-assign shares = %v/%v, want YAML 0.3/0.2", cfg.Routing.AutoAssignMaxNodeShare, cfg.Routing.AutoAssignMaxMigrationShare)
	}
}

func TestLoadRejectsInvalidAutoAssignShareEnvironment(t *testing.T) {
	for _, name := range []string{AutoAssignMaxNodeShareEnv, AutoAssignMaxMigrationShareEnv} {
		for _, value := range []string{"nope", "30%", "0.03", "1.5", "-0.1", "NaN", "Inf"} {
			t.Run(name+"="+value, func(t *testing.T) {
				path := writeAutoAssignShareConfig(t)
				t.Setenv(AutoAssignMaxNodeShareEnv, "")
				t.Setenv(AutoAssignMaxMigrationShareEnv, "")
				t.Setenv(name, value)

				_, err := Load(path)
				if err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("Load error = %v, want startup failure naming %s", err, name)
				}
			})
		}
	}
}
