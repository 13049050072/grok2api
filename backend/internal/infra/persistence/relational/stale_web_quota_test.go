package relational

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
)

func TestListStaleWebQuotaAccountIDsPutsNeverSyncedFirst(t *testing.T) {
	assertStaleWebQuotaOrder(t, openTestDatabase(t))
}

func TestPostgresListStaleWebQuotaAccountIDsPutsNeverSyncedFirst(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	ctx := context.Background()
	database, err := OpenPostgres(ctx, dsn, 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	assertStaleWebQuotaOrder(t, database)
}

func assertStaleWebQuotaOrder(t *testing.T, database *Database) {
	t.Helper()
	ctx := context.Background()
	repo := NewAccountRepository(database)
	now := time.Now().UTC().Truncate(time.Second)
	suffix := now.UnixNano()
	create := func(name string, syncedAt *time.Time) uint64 {
		t.Helper()
		stored, _, err := repo.UpsertByIdentity(ctx, account.Credential{
			Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: name, SourceKey: fmt.Sprintf("stale-quota-%s-%d", name, suffix),
			EncryptedAccessToken: "token", Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 1, MaxConcurrent: 8,
		})
		if err != nil {
			t.Fatal(err)
		}
		if syncedAt != nil {
			if err := repo.SaveQuotaWindows(ctx, stored.ID, account.WebTierAuto, *syncedAt, []account.QuotaWindow{{
				AccountID: stored.ID, Mode: "auto", Remaining: 1, Total: 1, WindowSeconds: 3600, SyncedAt: syncedAt, Source: account.QuotaSourceUpstream,
			}}); err != nil {
				t.Fatal(err)
			}
		}
		return stored.ID
	}
	older, old, fresh := now.Add(-72*time.Hour), now.Add(-48*time.Hour), now
	syncedOld := create("synced-old", &old)
	syncedOlder := create("synced-older", &older)
	neverSynced := create("never-synced", nil)
	recent := create("recent", &fresh)

	ids, err := repo.ListStaleWebQuotaAccountIDs(ctx, now.Add(-24*time.Hour), 1000)
	if err != nil {
		t.Fatal(err)
	}
	position := make(map[uint64]int, len(ids))
	for index, id := range ids {
		position[id] = index
	}
	for _, id := range []uint64{neverSynced, syncedOlder, syncedOld} {
		if _, ok := position[id]; !ok {
			t.Fatalf("stale account %d missing from %v", id, ids)
		}
	}
	if _, ok := position[recent]; ok {
		t.Fatalf("recently synced account %d listed: %v", recent, ids)
	}
	if !(position[neverSynced] < position[syncedOlder] && position[syncedOlder] < position[syncedOld]) {
		t.Fatalf("stale order = %v, want never-synced %d, then %d, then %d", ids, neverSynced, syncedOlder, syncedOld)
	}
}
