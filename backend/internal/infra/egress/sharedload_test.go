package egress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
	"golang.org/x/sync/singleflight"
)

func waitForSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func TestSharedLoadDetachesWorkFromStartingCaller(t *testing.T) {
	var group singleflight.Group
	started := make(chan struct{})
	release := make(chan struct{})
	observed := make(chan error, 1)
	first, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := sharedLoad(first, &group, "key", time.Minute, func(ctx context.Context) (string, error) {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Minute {
				observed <- fmt.Errorf("shared load deadline = %v, %v", deadline, ok)
				return "", nil
			}
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
			observed <- ctx.Err()
			return "shared", nil
		})
		firstDone <- err
	}()
	waitForSignal(t, started, "shared load did not start")
	cancel()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled caller kept waiting for the shared load")
	}
	close(release)
	select {
	case err := <-observed:
		if err != nil {
			t.Fatalf("shared load inherited the caller cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shared load did not finish")
	}
}

func TestSharedLoadDoesNotStartWorkForCancelledCaller(t *testing.T) {
	var group singleflight.Group
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sharedLoad(ctx, &group, "key", time.Minute, func(context.Context) (string, error) {
		t.Error("load started for a caller that had already given up")
		return "", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestSharedLoadReturnsPanicAsError(t *testing.T) {
	var group singleflight.Group
	_, err := sharedLoad(context.Background(), &group, "key", time.Minute, func(context.Context) (string, error) {
		panic("solver exploded")
	})
	if err == nil || !strings.Contains(err.Error(), "solver exploded") {
		t.Fatalf("error = %v", err)
	}
}

type cancellationAwareRepository struct {
	egressRepositoryTestStub
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func newCancellationAwareRepository(nodes ...domain.Node) *cancellationAwareRepository {
	return &cancellationAwareRepository{
		egressRepositoryTestStub: egressRepositoryTestStub{nodes: nodes},
		started:                  make(chan struct{}, 4),
		release:                  make(chan struct{}),
	}
}

func (r *cancellationAwareRepository) wait(ctx context.Context) error {
	r.calls.Add(1)
	select {
	case r.started <- struct{}{}:
	default:
	}
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *cancellationAwareRepository) ListEgressNodes(ctx context.Context, scope domain.Scope, sort repository.SortQuery) ([]domain.Node, error) {
	if err := r.wait(ctx); err != nil {
		return nil, err
	}
	return r.egressRepositoryTestStub.ListEgressNodes(ctx, scope, sort)
}

func (r *cancellationAwareRepository) GetEgressOperationsConfig(ctx context.Context) (domain.OperationsConfig, error) {
	if err := r.wait(ctx); err != nil {
		return domain.OperationsConfig{}, err
	}
	return domain.DefaultOperationsConfig(), nil
}

// assertLoadOutlivesCancelledStarter cancels the caller that started a shared
// repository read, then checks that a later caller is served by that same read.
func assertLoadOutlivesCancelledStarter(t *testing.T, repository *cancellationAwareRepository, load func(context.Context) error) {
	t.Helper()
	first, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- load(first) }()
	waitForSignal(t, repository.started, "shared repository read did not start")
	cancel()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled caller kept waiting for the shared read")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- load(context.Background()) }()
	close(repository.release)
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second caller did not finish")
	}
	if calls := repository.calls.Load(); calls != 1 {
		t.Fatalf("repository reads = %d, want the cancelled caller's read to be reused", calls)
	}
}

func TestNodeSnapshotLoadOutlivesCancelledStarter(t *testing.T) {
	repository := newCancellationAwareRepository(domain.Node{ID: 1, Scope: domain.ScopeWeb, Enabled: true})
	manager := NewManager(repository, nil)
	assertLoadOutlivesCancelledStarter(t, repository, func(ctx context.Context) error {
		values, err := manager.listNodes(ctx, domain.ScopeWeb, time.Now().UTC())
		if err == nil && len(values) != 1 {
			return fmt.Errorf("nodes = %#v", values)
		}
		return err
	})
}

func TestOperationsConfigLoadOutlivesCancelledStarter(t *testing.T) {
	repository := newCancellationAwareRepository()
	manager := NewManager(repository, nil)
	assertLoadOutlivesCancelledStarter(t, repository, func(ctx context.Context) error {
		_, supported, err := manager.loadOperationsConfig(ctx, time.Now().UTC())
		if err == nil && !supported {
			return errors.New("operations config reported unsupported")
		}
		return err
	})
}

type blockingClearanceSolver struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	ctxErrs []error
}

func (s *blockingClearanceSolver) Solve(ctx context.Context, _ ClearanceConfig, _ string) (clearanceSolution, error) {
	call := s.calls.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	s.mu.Lock()
	s.ctxErrs = append(s.ctxErrs, ctx.Err())
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return clearanceSolution{}, err
	}
	return clearanceSolution{Cookies: fmt.Sprintf("cf_clearance=value-%d", call), UserAgent: "Chrome/146 test"}, nil
}

func TestClearanceRefreshOutlivesCancelledStartingRequest(t *testing.T) {
	cipher, err := security.NewCipher("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	repository := &synchronizedEgressRepository{node: domain.Node{ID: 1, Name: "web", Scope: domain.ScopeWeb, Enabled: true, Health: 1}}
	solver := &blockingClearanceSolver{started: make(chan struct{}, 4), release: make(chan struct{})}
	manager := NewManager(repository, cipher)
	manager.solver = solver
	manager.UpdateClearanceConfig(ClearanceConfig{Mode: "flaresolverr", FlareSolverrURL: "http://solver", TargetURL: "https://grok.com", Timeout: 5 * time.Second, RefreshInterval: time.Hour})

	first, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		lease, err := manager.Acquire(first, domain.ScopeWeb, "account")
		if lease != nil {
			lease.Release()
		}
		firstDone <- err
	}()
	waitForSignal(t, solver.started, "clearance solve did not start")
	cancel()
	select {
	case err := <-firstDone:
		if err == nil {
			t.Fatal("cancelled request acquired a lease")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled request kept waiting for the shared clearance refresh")
	}

	type acquired struct {
		lease *Lease
		err   error
	}
	secondDone := make(chan acquired, 1)
	go func() {
		lease, err := manager.Acquire(context.Background(), domain.ScopeWeb, "account")
		secondDone <- acquired{lease: lease, err: err}
	}()
	close(solver.release)
	var second acquired
	select {
	case second = <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second request did not finish")
	}
	if second.err != nil {
		t.Fatal(second.err)
	}
	defer second.lease.Release()
	if !strings.Contains(second.lease.CFCookies, "value-1") || solver.calls.Load() != 1 {
		t.Fatalf("second request cookies=%q solves=%d, want the first solve reused", second.lease.CFCookies, solver.calls.Load())
	}
	solver.mu.Lock()
	ctxErrs := append([]error(nil), solver.ctxErrs...)
	solver.mu.Unlock()
	if len(ctxErrs) != 1 || ctxErrs[0] != nil {
		t.Fatalf("solver context errors = %v", ctxErrs)
	}
	repository.mu.Lock()
	persisted := repository.node.ClearanceRefreshedAt != nil
	repository.mu.Unlock()
	if !persisted {
		t.Fatal("clearance solved for a cancelled request was not persisted")
	}
}
