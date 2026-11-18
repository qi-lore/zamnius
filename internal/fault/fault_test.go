package fault_test

import (
	"testing"

	"github.com/qi-lore/zamnius/internal/fault"
)

func TestHitIsNoOpWhenDisarmed(t *testing.T) {
	t.Parallel()

	fault.Disarm()
	// Must not panic: an unarmed point is a map lookup in production.
	fault.Hit(fault.BeforeCommit)
	fault.Hit(fault.AfterCommit)
}

func TestHitPanicsWhenArmed(t *testing.T) {
	t.Parallel()

	fault.Arm(fault.BeforeCommit)
	t.Cleanup(fault.Disarm)

	if !fault.Armed(fault.BeforeCommit) {
		t.Fatal("armed point not reported as armed")
	}
	if fault.Armed(fault.AfterCommit) {
		t.Fatal("an unarmed point reported as armed")
	}

	defer func() {
		recovered := recover()
		triggered, ok := fault.AsTriggered(recovered)
		if !ok {
			t.Fatalf("recovered %v, want *fault.Triggered", recovered)
		}
		if triggered.Name != fault.BeforeCommit {
			t.Errorf("triggered %q, want %q", triggered.Name, fault.BeforeCommit)
		}
		if triggered.Error() == "" {
			t.Error("Triggered.Error() must describe the point")
		}
	}()
	fault.Hit(fault.BeforeCommit)
}

func TestDisarmClearsEverything(t *testing.T) {
	t.Parallel()

	fault.Arm(fault.BeforeLeaseRenew, fault.AfterArtifactRename)
	fault.Disarm()
	if fault.Armed(fault.BeforeLeaseRenew) || fault.Armed(fault.AfterArtifactRename) {
		t.Fatal("Disarm left a point armed")
	}
}
