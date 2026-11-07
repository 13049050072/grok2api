package relational

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/admin"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestAdminSessionRotateRecordsPreviousTokenHash(t *testing.T) {
	assertAdminSessionRotationTracksPreviousHash(t, context.Background(), openTestDatabase(t), "rotation-admin")
}

func TestPostgresAdminSessionRotateRecordsPreviousTokenHash(t *testing.T) {
	database := openPostgresSessionTestDatabase(t)
	assertAdminSessionRotationTracksPreviousHash(t, context.Background(), database, fmt.Sprintf("rotation-admin-%d", time.Now().UnixNano()))
}

func TestInitializeSchemaAddsPreviousTokenHashToLegacySessions(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "legacy-sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	assertLegacySessionsGainPreviousTokenHash(t, ctx, database, "legacy-admin")
}

func TestPostgresInitializeSchemaAddsPreviousTokenHashToLegacySessions(t *testing.T) {
	database := openPostgresSessionTestDatabase(t)
	assertLegacySessionsGainPreviousTokenHash(t, context.Background(), database, fmt.Sprintf("legacy-admin-%d", time.Now().UnixNano()))
}

func assertAdminSessionRotationTracksPreviousHash(t *testing.T, ctx context.Context, database *Database, username string) {
	t.Helper()
	adminValue := createSessionTestAdmin(t, ctx, database, username)
	sessions := NewAdminSessionRepository(database)
	first, second, third := sessionTokenHash(username, "first"), sessionTokenHash(username, "second"), sessionTokenHash(username, "third")
	created, err := sessions.Create(ctx, admin.Session{AdminID: adminValue.ID, RefreshTokenHash: first, ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if created.PreviousRefreshTokenHash != "" {
		t.Fatalf("new session previous hash = %q", created.PreviousRefreshTokenHash)
	}
	if _, err := sessions.GetByPreviousTokenHash(ctx, first); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("lookup before rotation error = %v", err)
	}

	if err := sessions.Rotate(ctx, created.ID, first, second, time.Now().UTC().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	current, err := sessions.GetByTokenHash(ctx, second)
	if err != nil || current.ID != created.ID || current.PreviousRefreshTokenHash != first {
		t.Fatalf("rotated session = %#v, err = %v", current, err)
	}
	if reused, err := sessions.GetByPreviousTokenHash(ctx, first); err != nil || reused.ID != created.ID {
		t.Fatalf("previous hash lookup = %#v, err = %v", reused, err)
	}
	if err := sessions.Rotate(ctx, created.ID, first, third, time.Now().UTC().Add(2*time.Hour)); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("stale rotation error = %v", err)
	}

	if err := sessions.Rotate(ctx, created.ID, second, third, time.Now().UTC().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if reused, err := sessions.GetByPreviousTokenHash(ctx, second); err != nil || reused.ID != created.ID {
		t.Fatalf("second rotation previous hash lookup = %#v, err = %v", reused, err)
	}
	if _, err := sessions.GetByPreviousTokenHash(ctx, first); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("only the latest previous hash should be kept, err = %v", err)
	}
}

func assertLegacySessionsGainPreviousTokenHash(t *testing.T, ctx context.Context, database *Database, username string) {
	t.Helper()
	adminValue := createSessionTestAdmin(t, ctx, database, username)
	db := database.db.WithContext(ctx)
	for _, statement := range []string{
		"DROP INDEX IF EXISTS idx_admin_sessions_previous_token_hash",
		"ALTER TABLE admin_sessions DROP COLUMN previous_refresh_token_hash",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if db.Migrator().HasColumn(&adminSessionModel{}, "previous_refresh_token_hash") {
		t.Fatal("legacy schema simulation kept previous_refresh_token_hash")
	}
	legacyHash := sessionTokenHash(username, "legacy")
	expiresAt := time.Now().UTC().Add(time.Hour)
	if err := db.Exec("INSERT INTO admin_sessions (admin_id, refresh_token_hash, expires_at, created_at) VALUES (?, ?, ?, ?)", adminValue.ID, legacyHash, expiresAt, time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := database.InitializeSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !db.Migrator().HasColumn(&adminSessionModel{}, "previous_refresh_token_hash") {
		t.Fatal("schema upgrade did not add previous_refresh_token_hash")
	}
	if !db.Migrator().HasIndex(&adminSessionModel{}, "idx_admin_sessions_previous_token_hash") {
		t.Fatal("schema upgrade did not add the previous token hash index")
	}
	sessions := NewAdminSessionRepository(database)
	legacy, err := sessions.GetByTokenHash(ctx, legacyHash)
	if err != nil || legacy.AdminID != adminValue.ID || legacy.PreviousRefreshTokenHash != "" {
		t.Fatalf("legacy session = %#v, err = %v", legacy, err)
	}
	if err := sessions.Rotate(ctx, legacy.ID, legacyHash, sessionTokenHash(username, "rotated"), expiresAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if reused, err := sessions.GetByPreviousTokenHash(ctx, legacyHash); err != nil || reused.ID != legacy.ID {
		t.Fatalf("upgraded session previous hash lookup = %#v, err = %v", reused, err)
	}
}

func openPostgresSessionTestDatabase(t *testing.T) *Database {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	ctx := context.Background()
	database, err := OpenPostgres(ctx, dsn, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return database
}

func createSessionTestAdmin(t *testing.T, ctx context.Context, database *Database, username string) admin.Admin {
	t.Helper()
	value, err := NewAdminRepository(database).Create(ctx, admin.Admin{Username: username, PasswordHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.db.Delete(&adminModel{}, value.ID).Error })
	return value
}

func sessionTokenHash(parts ...string) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(parts)))
	return hex.EncodeToString(sum[:])
}
