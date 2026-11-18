// Package fault provides named failure points that are inert unless a test arms
// them. Real providers cannot be interrupted at the exact window between a
// durable file and its committing row, which is the window recoverability must
// survive (research.md R10, ADR-0003).
//
// A hit panics rather than returning an error on purpose: an interruption must
// not be catchable by ordinary error handling inside the engine, otherwise the
// test would exercise an error path that no crash can produce.
package fault

import (
	"fmt"
	"sync"
)

// Named points. They sit at the boundaries the resume protocol reasons about.
const (
	BeforeArtifactRename = "before_artifact_rename"
	AfterArtifactRename  = "after_artifact_rename"
	BeforeCommit         = "before_commit"
	AfterCommit          = "after_commit"
	BeforeLeaseRenew     = "before_lease_renew"
)

// Triggered is the panic value a hit raises. Tests type-assert on it so an
// unrelated panic is never mistaken for an injected fault.
type Triggered struct {
	Name string
}

// Error implements error so a recovered panic can be reported like a failure.
func (t *Triggered) Error() string { return fmt.Sprintf("fault point triggered: %s", t.Name) }

var (
	mu    sync.RWMutex
	armed = make(map[string]bool)
)

// Arm enables the named points until Disarm is called.
func Arm(names ...string) {
	mu.Lock()
	defer mu.Unlock()
	for _, name := range names {
		armed[name] = true
	}
}

// Disarm disables every point. Tests must call this in a cleanup so a leftover
// armed point cannot panic an unrelated test in the same binary.
func Disarm() {
	mu.Lock()
	defer mu.Unlock()
	armed = make(map[string]bool)
}

// Armed reports whether a point is currently enabled.
func Armed(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return armed[name]
}

// Hit triggers the named point when armed. It is a no-op otherwise, so production
// builds pay a map lookup and nothing else.
func Hit(name string) {
	if !Armed(name) {
		return
	}
	panic(&Triggered{Name: name})
}

// AsTriggered reports whether a recovered value is an injected fault.
func AsTriggered(recovered any) (*Triggered, bool) {
	triggered, ok := recovered.(*Triggered)
	return triggered, ok
}
