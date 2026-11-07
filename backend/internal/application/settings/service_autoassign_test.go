package settings

import (
	"context"
	"testing"
)

func TestLoadPersistedKeepsStartupAutoAssignShares(t *testing.T) {
	cfg := testConfig(t)
	cfg.Routing.AutoAssignMaxNodeShare = 0.25
	cfg.Routing.AutoAssignMaxMigrationShare = 0.1
	repository := &runtimeSettingsRepositoryStub{value: toDomainConfig(cfg), found: true}

	loaded, _, _, err := LoadPersisted(context.Background(), cfg, repository)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Routing.AutoAssignMaxNodeShare != 0.25 || loaded.Routing.AutoAssignMaxMigrationShare != 0.1 {
		t.Fatalf("auto-assign shares = %v/%v, want startup 0.25/0.1", loaded.Routing.AutoAssignMaxNodeShare, loaded.Routing.AutoAssignMaxMigrationShare)
	}
}
