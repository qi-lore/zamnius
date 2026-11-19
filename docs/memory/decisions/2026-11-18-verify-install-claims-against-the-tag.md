# Prove a distribution claim by installing, not by reading the tree

**Date**: 2026-11-18 · **Where**: `README.md` §Build, `CHANGELOG.md` §0.1.0

## Decision

Any claim of the form "`go install …@<tag>` works" is verified by running it from an empty directory
with a fresh `GOBIN` **once the tag exists** — never inferred from the tag's existence or from a green
local build.

## Why it is durable

The release task had to tag a tree whose `README.md` and `CHANGELOG.md` asserted an install path that
had never been exercised: `v0.1.0` is the first tag in the module's history. A tag can exist while the
proxy has not indexed it, while `cmd/` has no main package, or while the module path and the
repository path disagree — none of which `go build ./...` catches.

## Evidence

`GOBIN=/tmp/… go install github.com/qi-lore/zamnius/cmd/zamnius@v0.1.0` downloaded `v0.1.0` and the
binary printed `zamnius 0.1.0 (api_version "1", schema 1)`. `README.md` §Build states that exact line.

## Where to look

`CHANGELOG.md` §Compatibility policy (SC-016); `contracts/cli.md` §2 for the envelope version.
