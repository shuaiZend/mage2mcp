# Documentation review — mage2mcp Phase 0

**Reviewer:** independent documentation reviewer (did not author any reviewed document).
**Date:** 2026-11-19.
**Baseline of record:** [research-baseline.md](../development/research-baseline.md) — facts F1–F26, the
`[V-live]` / `[V-docs]` / `[U]` legend, and the document conventions.
**Verdict:** **Approve with changes** (no Critical finding; 8 High, 21 Medium, 4 Low).

## 1. Scope and method

Files read in full: `README.md`, `README.zh-CN.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`,
`SECURITY.md`, `SUPPORT.md`, `docs/index.md`, `docs/development-plan.md`,
`docs/integrations/{magento,cloudflare,ga4}.md`, `docs/evidence/{magento,cloudflare,ga4}.md`, and
`docs/development/{agent-workflow,review-process,ci-cd,testing-strategy,license-policy,risk-register}.md`.

Cross-checks performed against the repository rather than against prose:

- `.github/workflows/ci.yml`, `security.yml`, `dependency-review.yml`, `release.yml` — job names,
  triggers, permissions, pinned tool versions.
- `package.json` — the two npm devDependency versions quoted by `ci-cd.md`.
- `docs/adr/0001`–`0013` — existence, status, and numbering referenced from other documents.
- Every relative markdown link in the reviewed set, resolved on disk: **0 broken links**.
- `npx markdownlint-cli2 "**/*.md" "#node_modules"` — **0 issues in 42 files**.
- `.markdown-link-check.json` — confirms the docs job checks relative links only, so the two results
  above are what the `docs` job sees.

Severity follows the review brief: Critical = a false claim that could mislead an operator into an
unsafe deployment; High = a factual contradiction or a missing required deliverable; Medium =
ambiguity, missing section, or stale statement; Low = wording or style. Markdownlint findings are out
of scope by instruction.

## 2. Summary

| Severity | Count | Character of the findings |
|---|---|---|
| Critical | 0 | No claim in the reviewed set would send an operator into an unsafe deployment; the status blockquotes and the `[U]` discipline hold almost everywhere |
| High | 8 | Self-contradiction between planning documents, CI documentation that contradicts the merged workflows, a misquoted baseline fact, and stale "to be written" text about a document that exists |
| Medium | 21 | Stale status statements, tag misuse, cross-references to content that is not in the target file, policy/enforcement drift, placeholder contacts |
| Low | 4 | Numbering, count mismatches, hedging wording |
| **Total** | **33** | |

## 3. Findings

| ID | Severity | Location | Finding | Suggested fix |
|---|---|---|---|---|
| D-01 | High | docs/development-plan.md:30 (also :115, :119) | §2 makes Phase 7's exit criterion "v0.1.0 tagged, signed, published, installable from the documented quickstart", but §7 assigns v0.1.0 to "Phase 1–2: Magento read + configurable write, MCP gateway, auth" and reserves v1.0.0 for "Phase 6–7 complete". README.md:359 also makes Phase 7 the v0.1.0 release, so two documents and one table disagree with one table in the same file. | Decide whether v0.1.0 follows Phase 2 or Phase 7, then align development-plan §2, §7 and the README roadmap; if v0.1.0 ships after Phase 2, §2's Phase 7 criterion becomes v1.0.0. |
| D-02 | High | docs/development/ci-cd.md:153 (also :39, :100-109); CONTRIBUTING.md:239 | The gap table states "No DCO or import-boundary check" and defers it to Phase 1, but `.github/workflows/ci.yml:88` defines a `dco` job that runs on every `pull_request` and fails a commit without `Signed-off-by`. §2.1 omits `dco` from the `ci.yml` job list and §3.1 omits it from the required-check table; CONTRIBUTING.md:239 says DCO "is checked by reviewers". | Add `dco` to §2.1, §3.1 and §4, delete the "No DCO" row, and correct CONTRIBUTING to say the sign-off is enforced by CI. |
| D-03 | High | docs/development/ci-cd.md:193 (also :200-201) | The permissions table gives the `gitleaks` job "`contents: read`, `security-events: write`", and the prose says "`security.yml` currently grants `security-events: write` workflow-wide, including to `gitleaks` and `govulncheck`". `security.yml` grants only `contents: read` and `actions: read` workflow-wide; `gitleaks` has no job-level permissions and uploads nothing; `security-events: write` is already scoped to `gosec` and `scorecard`. | Rewrite the table from `security.yml` and drop the "Phase 1 scopes it per job" promise, which is already true. |
| D-04 | High | docs/integrations/magento.md:528 | "**Decision record.** ADR-0003 (to be written separately) ratifies: …" — `docs/adr/0003-magento-client-design.md` exists, is `Status: Accepted`, dated 2026-11-18, and development-plan.md:40 lists it as a committed deliverable. | Replace the parenthetical with a link to ADR-0003 and state that it records those decisions. |
| D-05 | High | docs/integrations/ga4.md:529-532 (also docs/evidence/ga4.md:75, :81) | The "Contradictions with the baseline" table attributes to F18 the claims "`ecommercePurchases` cannot be combined with `eventCount`" and "`itemId`/`itemSku` cannot be combined with session-scoped metrics". Current F18 (research-baseline.md:46) says the opposite — it already records that both pairs were accepted and that `itemSku` does not exist — so the section contradicts a baseline it misquotes. The evidence file repeats the stale "(contradicts F18)" annotation on two rows. | Retitle the section "Findings that amended F18", quote the pre-amendment wording explicitly, or delete it; correct the two evidence annotations to "amended F18". |
| D-06 | High | docs/development/ci-cd.md:58 (also docs/development/license-policy.md:357, :438) | Dependency review is described as enforcing "no license on the deny list (GPL-2.0, GPL-3.0, AGPL-3.0, LGPL-2.1, LGPL-3.0, SSPL-1.0)". `.github/workflows/dependency-review.yml` uses `allow-licenses: 0BSD, Apache-2.0, BSD-2-Clause, BSD-3-Clause, CC0-1.0, ISC, MIT, MIT-0, Unlicense` and has no deny list; license-policy.md:424 already records the switch as resolved, so §10.4 and §15 contradict §14. | Describe the gate as an allowlist and quote its identifiers; remove the "Switch from `deny-licenses` to `allow-licenses`" recommendation and the "Deny list read" verification row. |
| D-07 | High | docs/development/license-policy.md:327 (also :334-343, :437) | §10.1 lists `gitleaks/gitleaks-action @v2` as "**Policy violation**", §10.2 says "Until one lands, section 14 records the exception", and §15 counts "1 proprietary (`gitleaks/gitleaks-action`)". The action is gone: `security.yml:44` installs `github.com/gitleaks/gitleaks/v8@v8.30.1`, and §14:421 records it "Resolved: MIT CLI v8.30.1, action removed". | Remove the action row and the §10.2 exception, and change §15 to "0 proprietary; MIT CLI v8.30.1 in use". |
| D-08 | High | docs/integrations/magento.md:147 (also :522) | The async row tags `PUT /async/V1/products/{sku}` as `[U]` with "registration unconfirmed (U5)", while baseline F26 records as `[V-docs]` that the route is **not** registered in 2.4.9 `webapi_async.xml`, and the same document's §15:507 reports only "Two core routes". Open question U5 asks a question the baseline already answers. | Mark the row `[V-docs]` "not registered in 2.4.9", close U5 with F26 as the resolution, and keep the fallback note. |
| D-09 | Medium | README.md:250 | "The design enforces the following properties:" introduces present-tense guarantees — "Write tools are disabled unless an operator explicitly enables a write profile", "Every tool call is recorded", "Credentials … are never logged or returned in tool output" — in a repository whose own status block (:7-12) says "There is no runnable server yet". A reader skimming the security section can take these as current behaviour. | Change to "The design specifies the following properties, none of which is implemented yet:" and keep the bullets. |
| D-10 | Medium | README.zh-CN.md:215 (also :77) | The Chinese security section omits the framing sentence "The design enforces the following properties:" entirely, and :77 renders "In the intended design, a tool call follows one path:" as "一次工具调用会经过以下路径：" — the two hedges that keep the bullets from reading as working behaviour. Everything else in the file is a faithful, complete translation. | Restore both sentences ("设计规定以下性质（尚未实现）：" and "在预期设计中，一次工具调用经过以下路径："). |
| D-11 | Medium | docs/integrations/cloudflare.md:215 | "At 10M requests/day an external destination costs roughly 6.5× an internal one". From the document's own table ($42.50–$87.50 vs $12.75–$26.25) and the $0.10/$0.03 rates, the ratio is 3.3×. | Correct to "about 3.3×" (or restate the comparison the 6.5 figure was meant to make). |
| D-12 | Medium | docs/integrations/cloudflare.md:274 | "Startup therefore does exactly two things, and the README says so", followed by "It cannot detect a write scope the operator silently added. That limitation is documented rather than papered over." README.md:207-210 documents the `Logs Write` profile but never states the two startup checks or the undetectable-write-scope limitation. | Add the limitation to the README Cloudflare section, or remove the cross-reference claim. |
| D-13 | Medium | docs/integrations/cloudflare.md:196-200 | The cost table puts "25 GB" in the "Included per account per month" column of both the internal-destination row and the external-destination row, which reads as a 50 GB allowance. F13 records a single 25 GB allowance per account, shared. | Merge the allowance into one row or add "shared across destinations" to both cells. |
| D-14 | Medium | docs/development/testing-strategy.md:156 | "Quota handling … concurrency is capped at 10", while docs/integrations/ga4.md:390 says "A per-property semaphore caps in-flight requests at 4 — below the observed limit of 10". The test would assert a cap the design does not implement. | Change the assertion to the documented cap of 4. |
| D-15 | Medium | docs/development/testing-strategy.md:22 (also :229-230, ci-cd.md:133) | Security tests are placed in `test/security`, while every other cross-cutting suite uses `tests/` (:19, :20, :21, :25) and CONTRIBUTING.md:186 assigns "Cross-cutting tests" to `tests/`. | Settle on `tests/security` and update the four references. |
| D-16 | Medium | CONTRIBUTING.md:175 (also :177, :182-185) | The ownership table gives the same role as owner and required reviewer for `internal/core/` (Architect / Architect), `internal/mcp/` (Architect / Architect), `internal/jobs/`, `internal/storage/`, `internal/observability/` and `cmd/`. That contradicts the same document's "No self-approval" rule (:277) and the different reviewers in agent-workflow.md:31-38. | Give each row an independent reviewer (for example Architect owns, Maintainer or a second agent reviews) and align the two tables. |
| D-17 | Medium | docs/development/ci-cd.md:164 | "`docker/setup-buildx-action` is also missing, so the multi-arch push cannot work", while §2.4:67 states the release job proves "a multi-arch image is pushed to GHCR". Both statements cannot hold. | State plainly that the image push is not yet functional, or add buildx and delete the caveat. |
| D-18 | Medium | docs/development/ci-cd.md:153 | The row quotes CONTRIBUTING as promising DCO "once CI is in place". CONTRIBUTING.md:29-45 and :256 state the sign-off requirement unconditionally and the phrase appears nowhere in the repository. | Quote the actual text or drop the quotation marks. |
| D-19 | Medium | docs/integrations/ga4.md:76 (also :79) | `[V-live]` is attached to a gateway policy decision, not an observation: "**Exposed.** Default and only reporting tool in v1 `[V-live]`" and "**Exposed with restrictions** (§8) `[V-live]`". The baseline defines `[V-live]` as "Confirmed against a live system", and a future exposure policy is not observable. | Drop those two tags, or move them onto the observed limits in the same row. |
| D-20 | Medium | docs/evidence/ga4.md:3 (also docs/integrations/ga4.md:507) | The evidence file publishes the account identifier `accounts/102070720`. Baseline rule 4 forbids customer data in evidence files, and SUPPORT.md:100 tells reporters not to include account or property IDs. The property ID alone identifies the environment and is already in F19. | Remove the account ID from both files. |
| D-21 | Medium | docs/development/license-policy.md:76 (also :78) | The Apache-2.0 cost table says the patent-termination cost is "Documented in the README license section", and the GPLv2 row says "documented in section 5 and in the README". README.md:378-382 contains the license name, the LICENSE and NOTICE pointers, and one SBOM sentence — neither caveat appears. | Add the two caveats to the README license section, or change the mitigation text to point only at this document. |
| D-22 | Medium | docs/development/license-policy.md:178 (also :200 vs :245, :267) | The inventory verdict for `segmentio/asm` is "Compatible in effect; variant not on allowlist" and the summary says "one MIT-0 variant that is not yet allowlisted", but §8.1 approves `MIT-0` ("Approved 2026-11-18") and §8.3 repeats the approval. | Change the verdict to "Compatible; MIT-0 allowlisted 2026-11-18" and drop the "not yet allowlisted" sentence. |
| D-23 | Medium | docs/development/license-policy.md:243 | §8.1 allows `MPL-2.0` "with review", but the enforced allowlist in `dependency-review.yml` omits MPL-2.0 and permits 0BSD, CC0-1.0 and Unlicense, which §8.1 does not list. Policy and enforcement disagree in both directions. | Make the workflow the single source of truth and reconcile §8.1 with it, including how MPL-2.0 and CC0-1.0 are treated. |
| D-24 | Medium | SECURITY.md:47 (also CODE_OF_CONDUCT.md:3, :64, SUPPORT.md:18) | Required community documents ship with placeholder contacts: "**Email and PGP fallback: TO BE CONFIGURED.**" and `conduct@mage2mcp.dev` marked "TO BE CONFIGURED" / "the mailbox above does not exist yet". A reporter following SECURITY.md's own instruction to avoid the conduct address has no non-GitHub fallback. | Configure the addresses before publication, or remove the promise and keep GitHub Security Advisories as the stated single channel. |
| D-25 | Medium | SECURITY.md:85-93 | The response targets are keyed to "High and Critical issues" (:89), but SECURITY.md never defines severity levels; the only definitions are in review-process.md:145-152, which SECURITY.md does not link. | Add a short severity table or link the definitions so a reporter can predict the triage category. |
| D-26 | Medium | docs/development/risk-register.md:47 | R28's mitigation is "disclaimer of affiliation in README", but README.md and README.zh-CN.md contain no affiliation or trademark statement (no mention of Adobe or "not affiliated"). The mitigation is claimed, not implemented. | Add a one-line disclaimer to both READMEs, or record R28 as open. |
| D-27 | Medium | docs/index.md:45-55 | The Development table lists seven documents but has no entry for `docs/reviews/`, although development-plan.md:38 links `reviews/phase-0-report.md` and ci-cd.md:274, review-process.md:167 and testing-strategy.md:279 all depend on review records living there. The entry point does not lead to the Phase 0 report or the review records. | Add a "Reviews and reports" row pointing at `reviews/phase-0-report.md` and the review-record convention. |
| D-28 | Medium | docs/development/ci-cd.md:135 (also :138-139) | The docs gate is described as "Enforced and required; red on `main` until referenced Phase 0 documents land", with the cause given as "`README.md` and `docs/index.md` link to Phase 0 documents still being written". Verified locally: markdownlint reports 0 issues in 42 files and every relative link in the reviewed set resolves, so the docs job is green and the stated cause no longer exists. | Restate the gate as green and remove the "red on `main`" explanation. |
| D-29 | Medium | README.md:154 (also :310) | The support matrix labels Cloudflare "GraphQL Analytics queries" as "Design in progress", while the compatibility matrix labels the same capability "Planned". The legend (:124-129) defines these as different states, and `docs/integrations/cloudflare.md` contains the design. | Use one status; "Design in progress" is the accurate one given §9 of the integration document. |
| D-30 | Low | docs/development/risk-register.md:37-38 | R29 and R30 sit between R24 and R25, and section 2 (:42) drops the "Review point" column that section 1 uses. | Move R29 and R30 after R28, or renumber; keep the columns consistent across sections. |
| D-31 | Low | docs/development/agent-workflow.md:113 | "The bypass is temporary: it is removed when a second maintainer joins", while ci-cd.md:277 says "when a second maintainer joins, or 90 days after `v0.1.0`, whichever comes first". | State the same removal trigger in both documents. |
| D-32 | Low | docs/development/ci-cd.md:258 | "Required status checks: `docs` today; four more in Phase 1", while §3.1:96-109 adds `go (1.26.x)`, `go (1.27.x)`, `govulncheck`, `gosec` and `guard` — five. | Change "four" to "five", or say "four plus `guard`". |
| D-33 | Low | docs/index.md:4 | "the documents below are the approved baseline for implementation". Approval is still pending: development-plan.md:3-4 makes Phase 1 conditional on maintainer approval of this plan and the Phase 0 report. | Say "the proposed baseline pending maintainer approval". |

## 4. The three most serious

**D-01 — the release plan contradicts itself and the README.** This is the finding most likely to
mislead a reader about what "done" means. `docs/development-plan.md` §2 puts v0.1.0 at the end of
Phase 7, §7 puts it at the end of Phase 2 and reserves v1.0.0 for Phase 6–7, and `README.md:359`
follows §2. An operator planning adoption from the README roadmap will expect a first usable release
only after hardening and release work; the plan's release table says a usable Magento gateway ships
after Phase 2. Both readings are defensible, which is exactly the problem.

**D-02 — CI documentation understates what CI already enforces, and overstates what it does not.**
`ci-cd.md` is normative ("a workflow that contradicts it is a bug in one of the two") yet it omits the
`dco` job that `ci.yml:88` runs on every pull request, lists DCO as a Phase 1 gap, and leaves it out of
the required-check table. The companion statement in `CONTRIBUTING.md:239` — that DCO is "checked by
reviewers" — is therefore also wrong. A contributor reading either document would conclude that a
missing sign-off is caught only by human review.

**D-05 — a verification document misquotes the baseline it is checking.** `docs/integrations/ga4.md`
§"Contradictions with the baseline" attributes two rules to F18 that F18 no longer contains, then
presents live observations as refutations of them. Because F18 is the single source of truth for GA4
compatibility, a reviewer comparing the two documents cannot tell whether the baseline is stale, the
integration document is stale, or the contradiction is real. The evidence file repeats the stale
annotation. This is the one finding that undermines the project's central claim — that every factual
statement traces to a tagged source.

## 5. Strengths

- **The status language is unusually honest where it matters most.** README.md:7-12 states that no
  server, image, or installation exists, and the support-matrix legend (:124-129) says outright that
  nothing is design-complete. SUPPORT.md:7-8, :111-114 and SECURITY.md:9-11 repeat the same message
  instead of implying availability.
- **The evidence files are the strongest documents in the set.** All three state what was attempted,
  against what, and that the result was "not run — credentials not provided"
  (`docs/evidence/magento.md:5-7`, `cloudflare.md:3-6`, `ga4.md:43`). They are honest about the limits
  of their own evidence (`ga4.md:173-181`), and none contains a token, a full upstream body, or a
  customer record.
- **The GA4 evidence is genuinely reproducible.** `docs/evidence/ga4.md:95-171` gives request bodies a
  reader can replay, and the "Totals vs row sum" finding (:55-56) documents a trap that most
  integrations get wrong.
- **The Cloudflare document separates four data planes and states what each cannot do**
  (`docs/integrations/cloudflare.md:40-95`), including the one-line summary at :93-95. It also
  records an unreconciled quota conflict instead of picking a number (:161-163).
- **The Magento document refuses to equate HTTP 200 with correctness**
  (`docs/integrations/magento.md:29-31`, §5.2, §15:510-512), and its endpoint map carries a tag per
  row rather than per section.
- **The read-only guarantee is specified as testable layers**, not as an intention
  (`cloudflare.md:235-248`), each with a named test.
- **Baseline discipline is visible**: F13, F14, F15, F16 and F17 are each re-cited in the Cloudflare
  verification log (`cloudflare.md:517-519`) with the date and environment.
- **The bilingual README is a real translation**, not a summary: all 20 sections, all 32 support-matrix
  rows, all 15 compatibility rows, all 14 documentation-map rows and all 8 roadmap rows are present,
  with the same status labels. Only the two hedges in D-10 are lost.
- **CONTRIBUTING covers the four required areas** — DCO with `git commit -s` and the exact
  `Signed-off-by` line (:29-45), branch prefixes (:119-131), test expectations (:204-234), and review
  rules including no self-approval (:263-290).
- **SECURITY covers the four required areas** — supported versions (:13-32), private reporting
  (:34-54), disclosure with a 90-day default embargo (:95-111), and severity-keyed response targets
  (:80-93), even though the severity definitions themselves are missing (D-25).
- **Placeholders are labelled rather than hidden.** `TO BE CONFIGURED` appears in SECURITY.md,
  CODE_OF_CONDUCT.md and SUPPORT.md instead of a fake address — a lesser evil, but still a gap
  (D-24).

## 6. Checked and found accurate

These were verified and need no change; they are listed so the next reviewer does not re-derive them.

- Go floor 1.26 and the CI matrix `1.26.x`/`1.27.x` agree across README.md:318, CONTRIBUTING.md:53-66,
  ci-cd.md:39, risk-register.md:13, license-policy.md:209 and `ci.yml:58`; no document states a 1.25
  floor.
- Apache-2.0 is consistent across README.md:380, CONTRIBUTING.md:26, LICENSE, NOTICE and
  license-policy.md:29-33.
- MCP protocol versions `2026-07-28` and `2025-11-25` match F3 in README.md:86-87, testing-strategy.md:166
  and README.zh-CN.md:75.
- Cloudflare pricing, record size, Logpush availability, bot-field entitlement and GraphQL limits match
  F13–F17 in README.md:196-201, :310-314 and cloudflare.md §4-§6; the worked example at
  cloudflare.md:210-213 is arithmetically correct (only the derived 6.5× in D-11 is wrong).
- GA4 quotas, property time zone and currency, `itemSku` absence, and the zero-ecommerce finding match
  F18–F21 in ga4.md §4, §6.1, §7.5 and §9.1.
- Magento endpoint paths for products, configurable, EAV, MSI, import and bulk status match F7–F11 in
  magento.md §3; GraphQL has no product write mutations, as F8 requires (magento.md:24-25, README.md:172).
- `docs/reviews/phase-0-report.md` exists and every relative link in the reviewed set resolves.
- `docs/adr/0001`–`0013` all exist, and every ADR number cited by development-plan.md §3 and the
  development documents resolves (only ADR-0003's "to be written" text in D-04 is stale).

## 7. Verdict

**Approve with changes.**

No finding is Critical: the documents that an operator would act on — README status block, SUPPORT,
SECURITY, the three integration documents — all say plainly that nothing is implemented, and the
`[U]` discipline holds in the places where a wrong claim would matter most (Magento behaviour, Cloudflare
entitlements, GA4 ecommerce).

The High findings are all fixable by editing prose, not by redesign: two are self-contradictions inside
`development-plan.md` and `ci-cd.md`, three are stale statements about artifacts that now exist or no
longer exist (ADR-0003, the gitleaks action, the docs gate), two are misdescriptions of the merged
workflows (`dco`, permissions), and one is a misquoted baseline fact.

Merge-blocking before Phase 0 exit: **D-01, D-02, D-04, D-05, D-06, D-07, D-08**, plus **D-24** if the
repository is made public before the contact addresses exist. D-03, D-09 through D-29, and the four Low
findings should be fixed in the same pass but need not gate anything on their own.
