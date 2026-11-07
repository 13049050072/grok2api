package relational

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	egressapp "github.com/chenyme/grok2api/backend/internal/application/egress"
	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
)

func TestMoveAutoEgressBindingsAppliesOnlyToObservedBindings(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	accounts := NewAccountRepository(database)
	nodes := NewEgressRepository(database)
	cipher := egressOperationsCipher(t)
	from := createHealthyEgressNode(t, ctx, nodes, cipher, "from", 0)
	to := createHealthyEgressNode(t, ctx, nodes, cipher, "to", 0)
	other := createHealthyEgressNode(t, ctx, nodes, cipher, "other", 0)
	stillAuto := createEgressOperationsAccount(t, ctx, accounts, "still-auto")
	nowManual := createEgressOperationsAccount(t, ctx, accounts, "now-manual")
	movedElsewhere := createEgressOperationsAccount(t, ctx, accounts, "moved-elsewhere")
	stillUnbound := createEgressOperationsAccount(t, ctx, accounts, "still-unbound")
	boundMeanwhile := createEgressOperationsAccount(t, ctx, accounts, "bound-meanwhile")
	old := time.Now().UTC().Add(-10 * time.Minute)
	for _, binding := range []struct {
		id   uint64
		node uint64
		mode account.EgressAssignmentMode
	}{
		{stillAuto.ID, from.ID, account.EgressAssignmentAuto},
		{nowManual.ID, from.ID, account.EgressAssignmentManual},
		{movedElsewhere.ID, other.ID, account.EgressAssignmentAuto},
		{boundMeanwhile.ID, other.ID, account.EgressAssignmentManual},
	} {
		if _, err := accounts.UpdateEgressBindings(ctx, account.ProviderBuild, []uint64{binding.id}, &binding.node, binding.mode, old); err != nil {
			t.Fatal(err)
		}
	}
	for index, id := range []uint64{stillAuto.ID, nowManual.ID} {
		if _, err := accounts.UpsertEgressLeaseBlock(ctx, account.EgressLeaseBlock{
			AccountID: id, NodeID: from.ID, Reason: "hard_tps", Version: "auto-binding-lease-" + string(rune('a'+index)), CooldownUntil: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	moved, err := accounts.MoveAutoEgressBindings(ctx, account.ProviderBuild, []uint64{stillAuto.ID, nowManual.ID, movedElsewhere.ID}, from.ID, to.ID, now)
	if err != nil || moved != 1 {
		t.Fatalf("moved=%d err=%v, want only the account still auto-bound to the observed node", moved, err)
	}
	assigned, err := accounts.MoveAutoEgressBindings(ctx, account.ProviderBuild, []uint64{stillUnbound.ID, boundMeanwhile.ID}, 0, to.ID, now)
	if err != nil || assigned != 1 {
		t.Fatalf("assigned=%d err=%v, want only the account that is still unbound", assigned, err)
	}

	want := map[uint64]struct {
		node uint64
		mode account.EgressAssignmentMode
	}{
		stillAuto.ID:      {to.ID, account.EgressAssignmentAuto},
		nowManual.ID:      {from.ID, account.EgressAssignmentManual},
		movedElsewhere.ID: {other.ID, account.EgressAssignmentAuto},
		stillUnbound.ID:   {to.ID, account.EgressAssignmentAuto},
		boundMeanwhile.ID: {other.ID, account.EgressAssignmentManual},
	}
	for id, expected := range want {
		actual, err := accounts.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if actual.EgressNodeID != expected.node || actual.EgressAssignmentMode != expected.mode {
			t.Fatalf("account %d binding = node %d mode %q, want node %d mode %q", id, actual.EgressNodeID, actual.EgressAssignmentMode, expected.node, expected.mode)
		}
	}
	blocks, err := accounts.ListEgressLeaseBlocks(ctx, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].AccountID != nowManual.ID {
		t.Fatalf("lease blocks = %#v, want only the skipped manual account's block", blocks)
	}
}

// racingAssignmentRepository commits concurrent binding edits right after the
// rebalance planner has read its account snapshot.
type racingAssignmentRepository struct {
	*AccountRepository
	once sync.Once
	race func()
}

func (r *racingAssignmentRepository) ListEgressAssignments(ctx context.Context, provider account.Provider) ([]account.Credential, error) {
	values, err := r.AccountRepository.ListEgressAssignments(ctx, provider)
	if err == nil && provider == account.ProviderBuild {
		r.once.Do(r.race)
	}
	return values, err
}

func TestRebalanceKeepsBindingsChangedDuringPlanning(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	accounts := NewAccountRepository(database)
	nodes := NewEgressRepository(database)
	cipher := egressOperationsCipher(t)
	unhealthy := createHealthyEgressNode(t, ctx, nodes, cipher, "unhealthy", 0)
	createHealthyEgressNode(t, ctx, nodes, cipher, "healthy", 0)
	manualTarget := createHealthyEgressNode(t, ctx, nodes, cipher, "manual-target", 0)
	unbound := createEgressOperationsAccount(t, ctx, accounts, "unbound")
	rebound := createEgressOperationsAccount(t, ctx, accounts, "rebound")
	evacuated := createEgressOperationsAccount(t, ctx, accounts, "evacuated")
	old := time.Now().UTC().Add(-10 * time.Minute)
	if _, err := accounts.UpdateEgressBindings(ctx, account.ProviderBuild, []uint64{rebound.ID, evacuated.ID}, &unhealthy.ID, account.EgressAssignmentAuto, old); err != nil {
		t.Fatal(err)
	}
	unhealthy.ProbeStatus = egress.ProbeStatusUnhealthy
	if _, err := nodes.UpdateEgressNode(ctx, unhealthy); err != nil {
		t.Fatal(err)
	}

	racing := &racingAssignmentRepository{AccountRepository: accounts, race: func() {
		if _, err := accounts.UpdateEgressBindings(ctx, account.ProviderBuild, []uint64{unbound.ID, rebound.ID}, &manualTarget.ID, account.EgressAssignmentManual, time.Now().UTC()); err != nil {
			t.Error(err)
		}
	}}
	service := egressapp.NewService(nodes, cipher, "test-browser", racing)
	result, err := service.RebalanceAccounts(ctx, true, false, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if result.Assigned != 0 || result.Rebalanced != 1 {
		t.Fatalf("rebalance result = %#v, want only the untouched account counted", result)
	}
	for _, id := range []uint64{unbound.ID, rebound.ID} {
		actual, err := accounts.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if actual.EgressNodeID != manualTarget.ID || actual.EgressAssignmentMode != account.EgressAssignmentManual {
			t.Fatalf("manual binding made during planning was overwritten: %#v", actual)
		}
	}
	actual, err := accounts.Get(ctx, evacuated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if actual.EgressNodeID == unhealthy.ID || actual.EgressNodeID == 0 || actual.EgressAssignmentMode != account.EgressAssignmentAuto {
		t.Fatalf("automatic account was not moved off the unhealthy node: %#v", actual)
	}
}

type countingAssignmentRepository struct {
	*AccountRepository
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *countingAssignmentRepository) ListEgressAssignments(ctx context.Context, provider account.Provider) ([]account.Credential, error) {
	r.calls.Add(1)
	if r.entered != nil {
		r.once.Do(func() {
			close(r.entered)
			<-r.release
		})
	}
	return r.AccountRepository.ListEgressAssignments(ctx, provider)
}

func TestEgressMaintenanceAssignmentRunsOnOneReplicaAtATime(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	accounts := NewAccountRepository(database)
	nodes := NewEgressRepository(database)
	cipher := egressOperationsCipher(t)
	if _, err := nodes.SaveEgressOperationsConfig(ctx, egress.OperationsConfig{
		ProbeIntervalSeconds: 900, AutoAssignEnabled: true, AssignmentIntervalSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	lock := memory.NewLockStore()
	replica := func(accounts *countingAssignmentRepository) *egressapp.Service {
		service := egressapp.NewService(nodes, cipher, "test-browser", accounts)
		service.SetAssignmentLock(lock)
		return service
	}
	firstAccounts := &countingAssignmentRepository{AccountRepository: accounts, entered: make(chan struct{}), release: make(chan struct{})}
	secondAccounts := &countingAssignmentRepository{AccountRepository: accounts}
	firstDone := make(chan error, 1)
	go func() { firstDone <- replica(firstAccounts).RunMaintenance(ctx) }()
	select {
	case <-firstAccounts.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first replica did not start assignment")
	}
	if err := replica(secondAccounts).RunMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := secondAccounts.calls.Load(); calls != 0 {
		t.Fatalf("second replica planned assignments %d times while the first held the lock", calls)
	}
	close(firstAccounts.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first replica did not finish")
	}
	thirdAccounts := &countingAssignmentRepository{AccountRepository: accounts}
	if err := replica(thirdAccounts).RunMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if thirdAccounts.calls.Load() == 0 {
		t.Fatal("assignment lock was not released after the first replica finished")
	}
}
