# Review process

**Status:** Phase 0 baseline. Applies to every pull request from Phase 0 onward.
**Owner:** Maintainer. Security-sensitive reviews also require the Security role.
**Related:** [Agent workflow](agent-workflow.md), [CI/CD](ci-cd.md),
[Testing strategy](testing-strategy.md), [Module boundaries](../module-boundaries.md),
[ADR-0007](../adr/0007-multi-agent-development.md), [CONTRIBUTING](../../CONTRIBUTING.md).

Review is the only control that catches a change which is wrong but passes CI: a
misread contract, a missing permission, a plausible-looking write path, a fixture that
happens to work. It is a security control, not a formality.

## 1. Purpose and non-negotiable rules

| Rule | What it means | Consequence of breaking it |
|---|---|---|
| Author is not the reviewer | The person or agent who wrote the change cannot approve it, even when they are the maintainer | The merge is invalid and is reverted or re-reviewed |
| No self-approval by proxy | An agent may not approve work it directed, nor may a second account be used to approve the same author | Treated as a governance violation; recorded in `docs/reviews/` |
| Critical and High findings block merge | A finding is fixed, or the change is withdrawn. It is never merged "to unblock" | Merge is reverted; the finding is reopened |
| Review the code, not the summary | The reviewer reads the diff, the tests, and the surrounding code. The PR description is a claim, not evidence | A review based on the description is void |
| One approval covers one commit range | New commits after approval require re-review of the delta | Stale approval is dismissed by the ruleset |
| Unresolved threads block merge | Every conversation is resolved or explicitly deferred with an issue | Merge is blocked by the ruleset |

The single-maintainer exception is narrow and recorded: the administrator bypass exists
only because there is no second human reviewer. Every use of it produces a review
record (Section 5) and is listed in the Phase 0 report.

## 2. Reviewer selection matrix

| Change type | Author role | Required reviewer | Additional |
|---|---|---|---|
| Documentation only | Any | Maintainer or Architect | Security if a security claim changes |
| Module feature, no contract change | Module owner | A different agent plus Maintainer | QA for test design |
| Public interface, DTO, tool schema, or error taxonomy | Module owner | Architect plus Maintainer | Security for auth-adjacent surfaces |
| Auth, RBAC, secrets, audit, confirmation | Security | Architect plus Maintainer | A second independent reviewer when available |
| CI, release, supply chain, workflow permissions | Platform or Maintainer | Security plus Maintainer | Architect for a new workflow |
| Dependency addition or upgrade | Any | Maintainer | License check and `govulncheck` |
| Security fix under embargo | Security | Maintainer | Reviewed privately, per Section 7 |
| Revert | Any | Maintainer | The original author is informed, not required |
| Ruleset or branch-protection change | Maintainer | Security | Review record required |

When no qualified reviewer exists, the fallback is an independent agent that did not
author or direct the change, plus a review record. "No reviewer available" is never a
reason to merge unreviewed; it is a reason to narrow the change until it can be
reviewed.

## 3. The review checklist

Every question is answerable from the diff. A "no" is a finding with a severity.

### 3.1 Correctness

| Question | A "no" means |
|---|---|
| Does the change do exactly what the linked issue or ADR says, and nothing else? | Scope creep or an undocumented decision |
| Is every new exported function reachable from a tool or a consumer, and tested? | Dead code or an untested surface |
| Are the boundary types right: money as a decimal string, time in UTC, IDs as strings? | A contract violation |
| Does config validation reject unknown keys and name the offending field without echoing a secret? | Fail-open configuration |
| Is the result deterministic for the same input, with no reliance on wall-clock time or map order? | A flaky or irreproducible behavior |

### 3.2 Error handling

| Question | A "no" means |
|---|---|
| Does every error path return a typed `errs.Error` with a stable `Code`? | An error the agent cannot act on |
| Is the cause kept in the log and out of the agent-visible message? | Possible information disclosure |
| Are upstream errors mapped per module by a table-driven test rather than passed through? | Raw vendor payloads reaching the agent |
| Does a partial failure (page 2 of 5) surface as an error, not a truncated success? | Silent data loss |
| Is `Retryable` accurate, and does the caller bound retries with backoff? | Retry storms against a vendor |
| Is `DeadlineExceeded` distinguished from `Canceled` and from an upstream timeout? | Wrong retry behavior |

### 3.3 Concurrency and resource lifecycle

| Question | A "no" means |
|---|---|
| Does every upstream call take the request context and honor cancellation? | Goroutine and connection leaks |
| Is every `resp.Body` and closer released on error paths too? | File descriptor exhaustion |
| Is package-level mutable state absent everywhere? | Hidden shared state and data races |
| Are shared caches and counters protected, and is the path covered by `-race`? | A data race |
| Is the semaphore or rate-limit token released with `defer` on panic paths? | Permanent capacity loss |
| Does shutdown drain in-flight work within the configured grace period? | Dropped writes and lost audit records |

### 3.4 Security

| Question | A "no" means |
|---|---|
| Is every caller-supplied value reaching a URL, header, path, or query allowlist-validated? | SSRF or injection |
| Is the tool registered with a permission, a timeout, `additionalProperties: false`, and an accurate `readOnlyHint`? | An unmapped or over-privileged tool |
| Is role, instance, and permission resolution independent of the request body and custom claims? | Privilege escalation |
| Are secrets redacted in logs, errors, metrics labels, and panic output? | Credential leakage |
| Is the egress host fixed by configuration rather than by request input? | SSRF |
| Does the threat model still describe every trust boundary this change touches? | An unanalyzed boundary |
| Is a new dependency justified, with license and maintenance status recorded? | Supply-chain risk |

### 3.5 API compatibility

| Question | A "no" means |
|---|---|
| Is every public interface, DTO, tool name, or schema change covered by an ADR? | An unreviewed contract change |
| Are new fields optional with explicit presence, never an overloaded zero value? | Ambiguous semantics |
| Is the error taxonomy extended without renumbering or renaming existing codes? | A breaking change for clients |
| Do the contract tests still pass for both the fake and the real service? | A contract that only one side honors |
| Is every stored-format change (audit record, cache key, cursor) versioned or migrated? | Corrupted state after deploy |

### 3.6 Tests

| Question | A "no" means |
|---|---|
| Do the tests fail if the behavior is reverted? | Tests that assert nothing |
| Is each new error branch covered? | Untested failure modes |
| Are tests deterministic: injected clock, no sleeps, no network, no ordering dependence? | Flakiness |
| Are fixtures free of real credentials, customer data, and account identifiers? | A data leak in the repository |
| Is each security control covered by a direct test, not incidental coverage? | An unproven control |
| Is the race detector clean for anything touching shared state? | A latent race |

### 3.7 Performance

| Question | A "no" means |
|---|---|
| Does the change add upstream calls per invocation, and is that within the documented budget? | Quota exhaustion |
| Is the result paginated or capped so an agent cannot request unbounded data? | Memory or quota blowup |
| Is every cache key namespaced by instance and schema version? | Cross-tenant leakage |
| Is there a benchmark, or a stated reason none applies, for a hot path? | An unmeasured regression |
| Is memory growth bounded relative to input size? | A denial-of-service vector |

### 3.8 Documentation

| Question | A "no" means |
|---|---|
| Does the PR update the module README, tool reference, configuration reference, and changelog for behavior changes? | Documentation drift |
| Are both READMEs updated when user-visible behavior changes? | An out-of-sync translation |
| Are new claims tagged `[V-live]`, `[V-docs]`, or `[U]` per the research baseline? | An unsupported claim |
| Is an ADR present for an architectural or interface decision? | An undocumented decision |
| Do internal links resolve and does the docs check pass? | A broken documentation build |

### 3.9 Licensing

| Question | A "no" means |
|---|---|
| Is every new dependency's license on the allowlist, with NOTICE updated if required? | A license violation |
| Is the commit signed off under the DCO? | A provenance gap |
| Is copied or vendored code identified with its origin and license? | An unattributed contribution |
| Are fixtures derived from vendor documentation attributed? | A copyright problem |

## 4. Severity definitions

| Severity | Definition | Project examples | Merge effect |
|---|---|---|---|
| Critical | Cross-tenant data, auth bypass, credential exposure, an unconfirmed write, or a supply-chain compromise | Role read from the request body; cache key without instance; token logged; new tool with no permission; unpinned action with write scope | Blocks unconditionally; no waiver |
| High | A security control that is missing or weak, a crash on untrusted input, or restricted data exposed to the wrong role | SSRF via media import; missing `additionalProperties: false`; panic on malformed JSON; PII in an error message | Blocks; waived only by the maintainer with Security review, a written rationale, and a tracking issue |
| Medium | Incorrect behavior, a missing test for a stated requirement, or a contract mismatch that does not leak data | Wrong error code; no pagination cap; a missing error-branch test; docs contradicting behavior | Blocks until fixed or tracked with an issue |
| Low | Naming, comments, formatting, or a documentation improvement | Inconsistent identifier; unclear comment; missing example | Does not block; may be deferred |

Severity is assigned by the reviewer and may be raised by the maintainer. Downgrading a
Critical or High finding requires the maintainer's written rationale in the PR and, for
High, Security review.

## 5. Recording a review

Findings are recorded as PR review comments, one finding per comment, prefixed with the
severity: `[Critical]`, `[High]`, `[Medium]`, `[Low]`. The author replies with the fix
or a reasoned disagreement; the reviewer resolves the thread. A finding that is
deferred gets an issue number in the thread.

A **review record** is required when the merge uses the administrator bypass, when the
ruleset changes, when a security test is quarantined, or when a release is approved by
its own author. It is committed at `docs/reviews/<pr-number>-review.md` before the
merge.

| Required content | Why |
|---|---|
| Reviewed commit range (base SHA to head SHA) | Proves what was actually reviewed |
| Reviewer identity: agent name and model, or human name | Makes the review attributable |
| Checklist used, with any item marked not applicable | Shows what was and was not examined |
| Findings, severity, and resolution | The audit trail |
| Explicit statement of what was **not** verified | Prevents implied coverage |
| CI evidence: run URL and the checks that were green | Ties the record to a result |
| Date and, for a bypass, the reason the bypass was used | Explains the exception |

```markdown
# Review record: PR #123

- Commit range: abc1234..def5678
- Reviewer: agent-name (model), or human name
- Bypass reason: single maintainer, no second human reviewer available
- Checklist: review-process.md Section 3, items 3.1-3.9

## Findings

| Severity | Finding | Resolution |
|---|---|---|
| High | Missing permission on the new tool | Fixed in def5678 |

## Not verified

- No real Magento instance was available; the configurable flow was verified against
  the mock server only.
```

## 6. Review SLAs and escalation

| Stage | Target | Notes |
|---|---|---|
| First response on a PR | 2 business days | Best effort, per SUPPORT.md |
| Re-review after changes | 1 business day | Only the delta is reviewed |
| Security-sensitive review | 1 business day, privately | Section 7 |
| Draft PR triage | 3 business days | A draft is not a review request |
| Stale PR | Closed after 60 days of inactivity | Reopenable |

Escalation: comment on the PR and mention the maintainer; if the disagreement is about
a contract, the Architect decides; if it is about a security control, Security decides
and the decision is recorded. A deadlock on an interface follows the conflict
resolution process in the [agent workflow](agent-workflow.md): stop the change, write
proposals, decide in an ADR. Merging to break a deadlock is prohibited.

## 7. Special cases

**Security-sensitive changes.** A vulnerability fix is developed in a private fork or a
private advisory branch, reviewed by the maintainer and Security, and merged with a
minimal commit message that does not describe the exploit. The default embargo is 90
days from acknowledgement, per [SECURITY.md](../../SECURITY.md). Public discussion,
public PRs, and detailed commit messages are prohibited until disclosure. The advisory
names the reviewers.

**Dependency additions.** The reviewer checks necessity (can the standard library do
it?), license, maintenance status and last release, transitive dependency count, CGO
use, and binary size impact. The PR must show `govulncheck` clean and a license result.
Dependabot PRs are reviewed too: the changelog of the new version, and for actions, any
change in permissions or inputs.

**Breaking changes.** Pre-1.0 a breaking change needs an ADR and a changelog entry with
a `BREAKING CHANGE:` footer. Post-1.0 it also needs a deprecation period of one minor
release, a migration note, and an announcement. The reviewer confirms that the ADR
names the affected tools and the migration path.

**Docs-only changes.** A docs-only PR still gets a review for accuracy, link integrity,
and claim tagging, but it takes the fast path: one reviewer, no security review, and no
test evidence required. A docs change that alters a security claim, a compatibility
claim, or an endpoint mapping is not docs-only; it needs the owning role.

**Reverts.** A revert names the original PR, the reason, and the incident or issue. It
is reviewed for exactness (it restores the previous state and nothing else) and merged
without delay. A security fix is never reverted without Security approval, because the
revert reopens the vulnerability.

## 8. Anti-patterns in review

| Anti-pattern | Why it is harmful | Do instead |
|---|---|---|
| Rubber-stamping: "LGTM" with no comment on a non-trivial diff | Removes the only control against a plausible but wrong change | Read the diff and state what was checked |
| Style nitpicking over correctness | Consumes the author's attention while a real defect passes | Report correctness and security first; style is Low |
| Reviewing only the description | The description is a claim; the diff is the change | Verify each claim against the code |
| Approving with unresolved threads | The finding is forgotten after merge | Resolve or convert to a tracked issue before approval |
| "I will fix it in a follow-up" for a High finding | Ships a known vulnerability | Fix now, or downgrade with a written rationale and Security review |
| Silent scope expansion during review | The reviewed change is not the merged change | Request a follow-up PR |
| Relitigating an accepted ADR in a PR | Blocks delivery and bypasses the decision process | Open a superseding ADR |
| Demanding a rewrite instead of a scoped fix | Costs review cycles and hides the actual defect | Name the smallest correct change |
| Blocking on personal preference without a documented rule | Makes review unpredictable and political | Cite the document or drop the comment |
| An agent reviewing work it directed | Self-approval by proxy | Route to an independent reviewer |

## 9. Open questions and decision record

| Question | Owner | Resolution point |
|---|---|---|
| Require two approvals once a second maintainer joins | Maintainer | When the second maintainer joins |
| Automate the checklist with a PR template bot | Platform | Phase 1 |
| Publish review records for every PR, not only bypass merges | Maintainer | Phase 6 |

| Decision | Record |
|---|---|
| Independent review, ownership routing, review records | [ADR-0007](../adr/0007-multi-agent-development.md) |
| Interface change process and required reviewers | [Module boundaries](../module-boundaries.md), Section 9 |
| PR rules, DCO, no self-approval | [CONTRIBUTING](../../CONTRIBUTING.md) |
| Bypass merge and ruleset settings | [CI/CD](ci-cd.md), Section 8 |
