# Audit programme — shared contract

This directory is the single source of truth for the v0.11.0 production-readiness
audit. Every report here uses the same finding format so the Chief Engineer can
merge, de-duplicate, and track them mechanically.

## Ground rules

1. **Code is the only authority.** A prior audit note, a README sentence, or a
   TODO comment is *not* evidence that a defect exists. Reproduce it in the
   current commit before recording it.
2. **No finding without a location.** Every finding cites `path/file.go:line`
   and quotes the code that causes it.
3. **Distinguish the four states.** A claim in the docs that the code does not
   implement is a documentation defect, not a code defect — and vice versa.
4. **Do not weaken tests.** Deleting a test, relaxing an assertion, or mocking
   the behaviour under test is a protocol violation, not a fix.
5. **Report honestly what could not be verified.** Mocked coverage is labelled
   `CONTRACT`; only a real Magento instance earns `INTEGRATION`.

## Finding format

```markdown
### <AREA>-<NN>: <one-line title>

- **Severity**: P0 | P1 | P2 | P3
- **State**: CONFIRMED | ALREADY-FIXED | NOT-REPRODUCIBLE | DOC-ONLY | PLANNED
- **Location**: `internal/magento/foo.go:123` (and any siblings)
- **Evidence**: what the code actually does today, quoted or paraphrased precisely.
- **Trigger**: the exact preconditions an attacker/operator needs.
- **Impact**: what breaks — data, money, confidentiality, availability.
- **Proposed fix**: the smallest change that removes the defect.
- **Test requirement**: the test that must exist before this is closed.
- **Verified by**: (filled in during independent review)
```

`AREA` is one of `SEC`, `MAG`, `REL`, `MCP`, `PERF`, `DOC`.

## Severity ladder

| Level | Meaning |
|---|---|
| **P0** | Security breach, privilege escalation, cross-instance data leak, duplicate charge/refund, data loss. |
| **P1** | Inventory inconsistency, wrong order state, catalog corruption, duplicated job execution, key business flow failure. |
| **P2** | Performance, concurrency, scalability, error-handling, API-semantics defects. |
| **P3** | Documentation, developer experience, maintainability, non-critical polish. |

## State ladder

| State | Meaning |
|---|---|
| **CONFIRMED** | Reproduced in the current commit; needs a fix. |
| **ALREADY-FIXED** | A prior audit flagged it; current code no longer has it. Cite the guard. |
| **NOT-REPRODUCIBLE** | Could not be made to happen; explain what you tried. |
| **DOC-ONLY** | The code is correct; a document claims otherwise. Fix the document. |
| **PLANNED** | Genuinely unimplemented and out of scope for this release; belongs in `known-limitations.md`. |

## Report ownership

| File | Owner | Content |
|---|---|---|
| `initial-audit-report.md` | Chief Engineer | Repository discovery, baseline, consolidated finding index |
| `security-audit-report.md` | Security auditor | `SEC-*` findings and the threat-model reconciliation |
| `magento-integration-review.md` | Magento engineer | `MAG-*` findings |
| `reliability-review.md` | Reliability engineer | `REL-*` findings and the operation-ledger design |
| `mcp-protocol-review.md` | MCP engineer | `MCP-*` findings and the verified tool inventory |
| `performance-review.md` | Performance engineer | `PERF-*` findings |
| `documentation-consistency.md` | Documentation engineer | `DOC-*` findings and the feature-status matrix |
| `architecture-review.md` | Chief Engineer | Module boundaries, contracts, dependency direction |
| `captain-findings.md` | Chief Engineer | `ARCH-*` findings established directly during architecture review |
| `remediation-plan.md` | Chief Engineer | Every finding → owner, fix, test, status |
| `independent-code-review.md` | Reviewers | Append-only cross-review log; every verdict bound to file hashes |
| `regression-test-report.md` | QA engineer | Commands, environment, pass/fail counts, the independent suite, blind spots |
| `known-limitations.md` | Chief Engineer | `DEFER` items, accepted risks, and the honest verification boundary |
| `release-readiness-report.md` | Chief Engineer | Final verdict and per-capability production assessment |

`perf-probe/` holds the reproducible measurement harness behind
`performance-review.md`. It targets the baseline commit and is excluded from the
default test run (`-tags probe`).

## Reading order

For a decision, read `release-readiness-report.md` first, then
`known-limitations.md`. For the evidence behind a claim, follow the finding id
into its domain report, then into `independent-code-review.md` for the verdict
and `regression-test-report.md` for the command that reproduces it.

## Baseline (commit at audit start)

Recorded in `regression-test-report.md` §1. Every agent re-runs the relevant
subset before claiming a fix works.
