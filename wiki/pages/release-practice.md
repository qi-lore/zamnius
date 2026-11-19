---
title: Release practice for this project
type: decision
sources: [S008, S010, S011, S012, S014]
updated: 2026-11-19
---

The first release (`v0.1.0`, 2026-11-18) settled four habits worth not relearning.

## The tag names the version the binary reports

`internal/cli.Version` is the release; the binary prints `zamnius 0.1.0 (api_version "1", schema 1)`
and the tag is `v0.1.0` (S010, S014). A tag that disagrees with the binary's own version string makes
`zamnius version` useless for support.

## The changelog is dated before the tag, not after

A released tree must not contain `## [0.1.0] - unreleased`, and must not claim "no git tag exists"
once one does. Both sentences were false the moment the tag was pushed, so the entry was dated and
the claim corrected in the integration PR that carried the tag (S014). The changelog is also the
compatibility record: additive changes are minor, removing or changing an exported signature is major,
and `internal/` is not part of the contract (S014).

## Distribution claims are proved by installing

`go install …@<tag>` is exercised from an empty `GOBIN` once the tag exists — a tag can exist while
the proxy has not indexed it, while `cmd/` has no main package, or while module path and repository
path disagree (S012).

## Green before tagging

The release PR must be green, and a timing-sensitive test that fails at 1.51× against a 1.5× bound is
a defect in the test, not a licence to loosen the bound (S011).

Knowledge produced this way is promoted, not hoarded: durable claims go to ADRs or the spec, and the
memory and wiki layers stay derived views that never override them (S008).
