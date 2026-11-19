# Timing assertion failed on CI at 1.51× while the branches did overlap

**Date**: 2026-11-18 · **Where**: `test/integration/execution_test.go` — `TestFanoutBranchesOverlap` (SC-006)

## What happened

CI (`go test ./... -race -count=1`, `ubuntu-latest`) failed with
`fan-out took 345.740581ms versus 229.228139ms for one branch; branches did not overlap` — 0.6% over
the 1.5× bound — while the stored attempt timestamps showed all three branches overlapping. The
engine was correct; the measurement was not.

## Why it is durable

The test compared the **whole-run wall clock** of a one-branch run against a three-branch run at a
150 ms branch delay. Both runs pay a fixed cost that does not scale with the delay (definition
snapshot, lease, attempt and artifact commits, join), and a fan-out pays it three times: ~195 ms vs
~79 ms on that runner, i.e. **77% of the baseline**. A timing assertion whose signal is comparable to
the fixed cost it does not control will flake on shared runners — and flake *in the direction of a
false regression*, which costs a release PR its green build.

## Rule

A ratio meant to prove concurrency must let the concurrent work dominate everything else: raise the
delay until fixed cost is a small fraction of the measurement (here 150 ms → 1 s, ~11%), and prove the
bound still has teeth by mutating the implementation (`MaxConcurrency: 1`), never by trusting a green
run.

## Evidence

- Failing job: run `44220468094`, job `134520749780` (release PR #3).
- Fix: PR #5, commit `8258191`.
- Mutation check at 1 s delay: 3.13 s vs 1.06 s (2.95×) → fails, as it must.

## Where to look

`test/integration/execution_test.go` (comment above `const delay`), `spec.md` SC-006.
