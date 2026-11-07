package egress

import (
	"math"
	"testing"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	domain "github.com/chenyme/grok2api/backend/internal/domain/egress"
)

func TestNormalizeAutoAssignShare(t *testing.T) {
	if got := normalizeAutoAssignShare(0); got != 0 {
		t.Fatalf("zero must stay off: %v", got)
	}
	if got := normalizeAutoAssignShare(0.03); got != 0 {
		t.Fatalf("below 0.05 must stay off: %v", got)
	}
	if got := normalizeAutoAssignShare(1.2); got != 0 {
		t.Fatalf("above 1 must stay off: %v", got)
	}
	if got := normalizeAutoAssignShare(0.3); got != 0.3 {
		t.Fatalf("valid share = %v", got)
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := normalizeAutoAssignShare(value); got != 0 {
			t.Fatalf("special float %v must stay off: %v", value, got)
		}
	}
}

func TestCapAutomaticAssignmentShareDisabledByDefault(t *testing.T) {
	nodes := []domain.Node{{ID: 1, AccountCapacity: 0}, {ID: 2, AccountCapacity: 40}}
	accounts := makeActiveAccounts(20)
	got := capAutomaticAssignmentShare(nodes, accounts, 0)
	if got[0].AccountCapacity != 0 || got[1].AccountCapacity != 40 {
		t.Fatalf("disabled share must not rewrite capacities: %#v", got)
	}
}

func TestCapAutomaticAssignmentShareAppliesFraction(t *testing.T) {
	nodes := []domain.Node{{ID: 1, AccountCapacity: 0}, {ID: 2, AccountCapacity: 40}}
	accounts := makeActiveAccounts(100)
	got := capAutomaticAssignmentShare(nodes, accounts, 0.30)
	if got[0].AccountCapacity != 30 {
		t.Fatalf("uncapped node should absorb at most 30: %#v", got[0])
	}
	if got[1].AccountCapacity != 30 {
		t.Fatalf("configured 40 should be lowered to 30: %#v", got[1])
	}
}

func TestCapAutomaticAssignmentSharePreservesOtherProviderLoad(t *testing.T) {
	nodes := []domain.Node{{ID: 1, AccountCapacity: 0, AssignedAccountCount: 30}}
	accounts := makeActiveAccounts(100)
	got := capAutomaticAssignmentShare(nodes, accounts, 0.30)
	if got[0].AccountCapacity != 60 {
		t.Fatalf("30 reserved slots plus 30 active provider slots = %#v", got[0])
	}
}

func TestAutoAssignmentMigrationLimitPreservesHistoricalCeilingWhenOff(t *testing.T) {
	if got := autoAssignmentMigrationLimit(makeActiveAccounts(5000), 0); got != -1 {
		t.Fatalf("disabled share must leave first-pass unbounded: %d", got)
	}
	if got := autoAssignmentMigrationLimit(makeActiveAccounts(100), 0.10); got != 10 {
		t.Fatalf("10%% of 100 active accounts = %d", got)
	}
}

func TestAutoAssignBoundsUseResolvedConfigOnly(t *testing.T) {
	t.Setenv("GROK2API_AUTO_ASSIGN_MAX_NODE_SHARE", "0.25")
	t.Setenv("GROK2API_AUTO_ASSIGN_MAX_MIGRATION_SHARE", "0.5")
	service := &Service{}
	service.ConfigureAutoAssignBounds(0.4, 0)
	if node, migration := service.autoAssignBounds(); node != 0.4 || migration != 0 {
		t.Fatalf("bounds = %v/%v, want configured 0.4/0 without re-reading env", node, migration)
	}
}

func makeActiveAccounts(n int) []accountdomain.Credential {
	accounts := make([]accountdomain.Credential, n)
	for i := range accounts {
		accounts[i] = accountdomain.Credential{Enabled: true, AuthStatus: accountdomain.AuthStatusActive}
	}
	return accounts
}
