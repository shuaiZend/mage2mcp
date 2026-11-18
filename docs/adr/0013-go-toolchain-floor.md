# ADR-0013: Go toolchain floor is 1.26

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect
- **Supersedes:** none

## Context

The official MCP Go SDK v1.8.0 requires `go 1.25.0`. During Phase 0 dependency
resolution, two planned direct dependencies were found to declare a higher floor:
`github.com/prometheus/client_golang` v1.25.0 and `github.com/redis/go-redis/v9`
v9.23.0 both declare `go 1.26.0`. Go supports the two most recent releases, so
1.25 is already outside the support window while 1.27 is current.

## Problem

Do we keep a Go 1.25 floor by pinning older dependency versions, or raise the
floor and track current dependencies?

## Options considered

1. **Keep `go 1.25.0`, pin `client_golang` v1.24.1 and `go-redis` v9.22.0.**
   Widest contributor reach, but freezes two security-sensitive libraries at
   older releases and forces future dependency work to fight the floor.
2. **Raise the floor to `go 1.26.0`, test 1.26.x and 1.27.x.** Current
   dependencies, a supported toolchain pair, and a simpler maintenance story.
3. **Require `go 1.27.x` only.** Simplest CI, but needlessly excludes 1.26 users
   while 1.26 remains supported.

## Decision

Adopt option 2.

- `go.mod` declares `go 1.26.0` with **no** `toolchain` directive: the pinned golangci-lint release is built with Go 1.26 and refuses to analyze a module that pins a newer toolchain. Contributors use their installed toolchain; CI tests 1.26.x and 1.27.x.
- CI matrix: `1.26.x` and `1.27.x`, with `GOTOOLCHAIN=local` so CI never silently
  downloads a different toolchain.
- The README, CONTRIBUTING, and quickstart state Go 1.26+ as the requirement.
- Dependency bumps that raise the floor again require a new ADR.

## Rationale

Pinning two core libraries to older releases to preserve a toolchain that Go
itself no longer supports trades real maintenance and security cost for a
marginal contributor benefit. Go's toolchain auto-download means a contributor on
an older install can still build the project, so the practical cost of the floor
is low.

## Consequences

### Positive

- Current, supported dependency versions.
- A supported CI matrix that matches Go's own policy.
- No conflict between the module directive and the dependency graph.

### Negative

- Contributors on Go 1.25 or earlier must rely on toolchain auto-download or
  upgrade.
- One more thing to update when the floor moves again.

### Security impact

Low. Tracking current dependency releases reduces exposure to unfixed issues in
pinned older versions.

### Maintenance cost

Low: one line in `go.mod` and two entries in the CI matrix, reviewed at each Go
release.

## Follow-up

- Phase 1: set `go 1.26.0` and `toolchain go1.27.4` in `go.mod`.
- Phase 1: add a CI check that fails if the `go` directive and the matrix drift.
- Revisit when Go 1.28 is released.

## References

- [CI/CD](../development/ci-cd.md)
- [License policy](../development/license-policy.md) section 6.3
