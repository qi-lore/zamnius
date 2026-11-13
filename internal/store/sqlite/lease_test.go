package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/store/sqlite"
)

func TestAcquireLeaseIsExclusive(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	lease, err := store.AcquireLease(ctx, run.ID, "owner-a", 30*time.Second)
	if err != nil {
		t.Fatalf("first AcquireLease() error = %v", err)
	}
	if lease.Owner != "owner-a" {
		t.Errorf("owner = %q, want owner-a", lease.Owner)
	}

	// Conformance test 2: a second acquisition while the lease is live fails.
	_, err = store.AcquireLease(ctx, run.ID, "owner-b", 30*time.Second)
	if err == nil {
		t.Fatal("second AcquireLease succeeded while the lease was live")
	}
	if !errors.Is(err, domain.ErrRunAlreadyOwned) {
		t.Fatalf("error = %v, want ErrRunAlreadyOwned", err)
	}
}

func TestConcurrentAcquireLeaseHasOneWinner(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	const contenders = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
	)
	for i := 0; i < contenders; i++ {
		owner := "owner-" + string(rune('a'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.AcquireLease(ctx, run.ID, owner, 30*time.Second); err == nil {
				mu.Lock()
				winners = append(winners, owner)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("winners = %v, want exactly one", winners)
	}
}

func TestExpiredLeaseIsTakenOverWithRecordedEvidence(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	// A lease with a tiny TTL expires before the second acquisition.
	if _, err := store.AcquireLease(ctx, run.ID, "owner-a", time.Millisecond); err != nil {
		t.Fatalf("first AcquireLease() error = %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	lease, err := store.AcquireLease(ctx, run.ID, "owner-b", 30*time.Second)
	if err != nil {
		t.Fatalf("takeover AcquireLease() error = %v", err)
	}
	if lease.Owner != "owner-b" {
		t.Errorf("owner = %q, want owner-b", lease.Owner)
	}

	// Conformance test 3: the takeover is recorded with the previous owner.
	events, err := store.Events(ctx, run.ID, 0)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	var takeover *domain.Event
	for i := range events {
		if events[i].Type == string(domain.EventRunTakenOver) {
			takeover = &events[i]
		}
	}
	if takeover == nil {
		t.Fatal("no run.taken_over event recorded")
	}
	if !contains(string(takeover.Payload), "owner-a") {
		t.Errorf("takeover payload = %s, want the previous owner named", takeover.Payload)
	}

	decisions, err := store.Decisions(ctx, run.ID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	if len(decisions) != 1 || decisions[0].Kind != domain.DecisionTakeover {
		t.Fatalf("decisions = %+v, want one takeover decision", decisions)
	}
}

func TestRenewAndReleaseLease(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	lease, err := store.AcquireLease(ctx, run.ID, "owner-a", 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}

	renewed, err := store.RenewLease(ctx, lease, 60*time.Second)
	if err != nil {
		t.Fatalf("RenewLease() error = %v", err)
	}
	if !renewed.ExpiresAt.After(lease.ExpiresAt) {
		t.Errorf("renewed expiry %v is not after %v", renewed.ExpiresAt, lease.ExpiresAt)
	}

	if err := store.ReleaseLease(ctx, renewed); err != nil {
		t.Fatalf("ReleaseLease() error = %v", err)
	}
	loaded, err := store.LoadRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if loaded.OwnerID != nil || loaded.LeaseExpiresAt != nil {
		t.Fatalf("lease not released: owner=%v expiry=%v", loaded.OwnerID, loaded.LeaseExpiresAt)
	}

	// After release another process may take the run immediately.
	if _, err := store.AcquireLease(ctx, run.ID, "owner-b", 30*time.Second); err != nil {
		t.Fatalf("AcquireLease after release error = %v", err)
	}
}

func TestRenewLeaseFailsAfterTakeover(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	stale, err := store.AcquireLease(ctx, run.ID, "owner-a", time.Millisecond)
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := store.AcquireLease(ctx, run.ID, "owner-b", 30*time.Second); err != nil {
		t.Fatalf("takeover error = %v", err)
	}

	// A stale owner must learn it lost the run instead of continuing to write.
	if _, err := store.RenewLease(ctx, stale, 30*time.Second); !errors.Is(err, domain.ErrRunAlreadyOwned) {
		t.Fatalf("RenewLease after takeover error = %v, want ErrRunAlreadyOwned", err)
	}
}

func TestTwoStoresOnOneFileSeeOneOwner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "zamnius.db")
	ctx := context.Background()

	first, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	run := createRun(t, first, "plan")

	second, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if _, err := first.AcquireLease(ctx, run.ID, "owner-a", 30*time.Second); err != nil {
		t.Fatalf("first AcquireLease() error = %v", err)
	}
	if _, err := second.AcquireLease(ctx, run.ID, "owner-b", 30*time.Second); !errors.Is(err, domain.ErrRunAlreadyOwned) {
		t.Fatalf("second store AcquireLease error = %v, want ErrRunAlreadyOwned", err)
	}
}

// TestConcurrentAcquireLeaseAcrossConnectionsHasOneWinner is the two-process case:
// two stores on one database file contend for the same run's lease, as two
// processes would. Exactly one must win and the other must be told the run is
// owned, never that the database is locked. A deferred transaction would fail this
// by returning SQLITE_BUSY on the read-then-write upgrade, so this test pins the
// BEGIN IMMEDIATE decision in Open (ADR-0004, ADR-0009).
func TestConcurrentAcquireLeaseAcrossConnectionsHasOneWinner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "zamnius.db")
	ctx := context.Background()

	first, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	run := createRun(t, first, "plan")

	second, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	contenders := []struct {
		owner string
		store *sqlite.Store
	}{
		{owner: "owner-a", store: first},
		{owner: "owner-b", store: second},
	}

	start := make(chan struct{})
	results := make(chan error, len(contenders))
	var wg sync.WaitGroup
	for _, contender := range contenders {
		contender := contender
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := contender.store.AcquireLease(ctx, run.ID, contender.owner, 30*time.Second)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	winners, losers := 0, 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, domain.ErrRunAlreadyOwned):
			losers++
		default:
			t.Errorf("contender error = %v, want nil or ErrRunAlreadyOwned", err)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("winners = %d, losers = %d, want exactly one of each", winners, losers)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
