# ADR-0011: Configuration and multi-instance model

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect
- **Supersedes:** none

## Context

A single deployment may serve several Magento stores, Cloudflare accounts and
zones, and GA4 properties. Credentials differ per instance. Operators deploy with
Docker Compose or Kubernetes and expect environment-based secrets. A single bad
instance must not prevent the service from starting.

## Problem

How is configuration expressed, validated, and isolated per instance, and how do
secrets reach the process without entering the image or the repository?

## Options considered

1. **Environment variables only.** Simple, but unmanageable for nested instances
   and lists.
2. **YAML file only.** Readable, but encourages committing secrets and complicates
   container overrides.
3. **YAML with environment overrides and secret references.** Verbose but explicit.
4. **Database-stored configuration edited through an API.** Dynamic, but requires a
   bootstrap credential and an admin surface before the gateway is trustworthy.

## Decision

Adopt option 3.

- YAML defines structure; environment variables override scalar values and supply
  secrets by reference (`${VAR}`).
- Unknown keys are rejected. Validation runs at startup and reports every problem,
  not just the first.
- Every instance has a stable `id`. Tools take `instance` as a required parameter
  when more than one instance of that module exists.
- An invalid or unreachable instance is disabled individually; the process starts
  if at least one module is usable, and `/readyz` reports degraded.
- Secrets are resolved from environment or a secret-manager adapter; literal
  secrets in YAML are rejected.
- Configuration is mounted read-only. No configuration is written back by the
  server.

## Rationale

Nested instance lists do not fit environment variables, while secrets must not
live in files. The hybrid keeps structure reviewable and secrets injectable.
Per-instance isolation directly satisfies the requirement that one broken data
source must not take down the others.

## Consequences

### Positive

- Clear, reviewable configuration with no secrets in the repository.
- Partial availability instead of all-or-nothing startup.
- Instance-scoped tools are unambiguous.

### Negative

- Two configuration sources require precedence documentation.
- Startup validation is more complex.

### Security impact

Secret references keep credentials out of images and Git. Startup validation
prevents accidentally running with a partially applied configuration. The
gateway must never echo resolved secrets in validation errors.

### Maintenance cost

Moderate: schema changes must be documented and validated.

## Follow-up

- Phase 1: configuration schema, validation, and examples.
- Phase 1: document a Kubernetes secret and a Docker secret pattern.

## References

- [Architecture](../architecture.md)
- [Database design](../database-design.md)
