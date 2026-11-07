package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

// blockingOAuthAdapter returns an adapter whose token endpoint holds every exchange until
// released, failing it only if the request context itself is cancelled.
func blockingOAuthAdapter(t *testing.T) (*Adapter, *security.Cipher, chan context.Context, chan struct{}) {
	t.Helper()
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan context.Context, 4)
	release := make(chan struct{})
	adapter := NewAdapter(Config{}, cipher)
	adapter.oauth.http = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests <- request.Context()
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-release:
			return oauthResponse(http.StatusOK, `{"access_token":"fresh-access","refresh_token":"rotated-rt","expires_in":3600}`), nil
		}
	})}
	return adapter, cipher, requests, release
}

func TestRefreshExchangeOutlivesCancelledCaller(t *testing.T) {
	adapter, cipher, requests, release := blockingOAuthAdapter(t)
	encryptedRefresh, err := cipher.Encrypt("original-rt")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		value provider.RefreshedCredential
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := adapter.RefreshCredential(ctx, accountdomain.Credential{EncryptedRefreshToken: encryptedRefresh})
		done <- outcome{value: value, err: err}
	}()
	requestCtx := <-requests
	cancel()
	if requestCtx.Err() != nil {
		t.Fatal("caller cancellation aborted a started refresh exchange")
	}
	close(release)
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	rotated, err := cipher.Decrypt(result.value.EncryptedRefreshToken)
	if err != nil || rotated != "rotated-rt" {
		t.Fatalf("rotated refresh token = %q, error = %v", rotated, err)
	}

	if _, err := adapter.RefreshCredential(ctx, accountdomain.Credential{EncryptedRefreshToken: encryptedRefresh}); !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled caller error = %v", err)
	}
	if len(requests) != 0 {
		t.Fatal("already cancelled caller started an exchange")
	}
}

func TestPrepareImportedCredentialExchangeOutlivesCancelledCaller(t *testing.T) {
	adapter, _, requests, release := blockingOAuthAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		value provider.CredentialSeed
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := adapter.PrepareImportedCredential(ctx, provider.CredentialSeed{RefreshToken: "original-rt"})
		done <- outcome{value: value, err: err}
	}()
	requestCtx := <-requests
	cancel()
	if requestCtx.Err() != nil {
		t.Fatal("caller cancellation aborted a started import exchange")
	}
	close(release)
	result := <-done
	if result.err != nil || result.value.RefreshToken != "rotated-rt" || result.value.AccessToken != "fresh-access" {
		t.Fatalf("prepared = %#v, error = %v", result.value, result.err)
	}

	if _, err := adapter.PrepareImportedCredential(ctx, provider.CredentialSeed{RefreshToken: "another-rt"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled caller error = %v", err)
	}
	if len(requests) != 0 {
		t.Fatal("already cancelled caller started an exchange")
	}
}

func TestRefreshTokenImportPersistsExchangeInFlightWhenCancelled(t *testing.T) {
	adapter, cipher, requests, release := blockingOAuthAdapter(t)
	database, err := relational.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "rt-import-cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	service := accountapp.NewService(accounts, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result accountapp.ImportResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.ImportCredentials(ctx, []byte("rt=original-rt\n"))
		done <- outcome{result: result, err: err}
	}()
	<-requests
	cancel()
	close(release)
	var finished outcome
	select {
	case finished = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("import did not finish after cancellation")
	}
	if !errors.Is(finished.err, context.Canceled) || finished.result.Created != 1 {
		t.Fatalf("import result = %#v, error = %v", finished.result, finished.err)
	}
	stored, err := accounts.ListEnabled(context.Background(), accountdomain.ProviderBuild)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored accounts = %#v", stored)
	}
	refreshToken, err := cipher.Decrypt(stored[0].EncryptedRefreshToken)
	if err != nil || refreshToken != "rotated-rt" {
		t.Fatalf("stored refresh token = %q, error = %v", refreshToken, err)
	}
}
