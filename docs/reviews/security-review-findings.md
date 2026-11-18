# Independent security review findings — mage2mcp Phase 0

**Reviewer:** Independent security reviewer (not an author of the documents under review).
**Date:** 2026-11-18/19. **Baseline:** [research-baseline.md](../development/research-baseline.md).
**Scope:** [threat-model.md](../security/threat-model.md), [security-review.md](../security/security-review.md),
[ADR-0006](../adr/0006-authentication.md), [ADR-0012](../adr/0012-observability-and-audit.md),
[ADR-0014](../adr/0014-confirmation-approval-and-bindings.md), [SECURITY.md](../../SECURITY.md), the four
workflows in `.github/workflows/`, [mcp-tools.md](../mcp-tools.md), and the Magento, Cloudflare and GA4
integration designs.
**Method:** adversarial read of each claimed control for testability, layer and effect; cross-document
consistency check; JWT pitfall checklist (RFC 8725); agent-specific abuse review; workflow review.
**Code status:** no implementation exists (`security-review.md:5`). Every finding is a design or
documentation defect, not a runtime vulnerability.

**Snapshot.** Line numbers and hashes were verified at 00:27 HKT. The tree was being edited during the
review (see §6), so findings are pinned to these digests:

```text
threat-model 3cf9287a1883   security-review c7da0b240631   ADR-0006 ab3b10d2d608
ADR-0012 aa221d7de364       ADR-0014 10f2489bb558          SECURITY a3cfc4dc70bd
mcp-tools 142f610f910e      magento 7bb53f0668b1           cloudflare 62e5face2f59
ga4 1ac4c1f25359            ci d08a7ce0a35f                security 7033619f7456
release 271852b25af6
```

## 1. Threat-class coverage against the fifteen mandatory classes

All fifteen classes have a section in the threat model, so the coverage claim in
`docs/reviews/phase-0-report.md:133` is literally true. It is not uniformly substantive: four classes have
no threat ID for their real failure mode, and two headline controls do not hold as written.

| # | Mandatory class | Section | Assessment | Gap |
|---|---|---|---|---|
| 1 | Prompt injection | §5.1 | Covered | Capability separation is false for shipped tools (F-28); approval diff is shaped by untrusted input |
| 2 | Tool parameter injection | §5.2 | Covered | Conflicting SKU grammars (F-15); unconstrained schema fields (F-14) |
| 3 | Privilege escalation | §5.3 | Covered | Admin can grant itself a data role; no dual control (F-27) |
| 4 | Multi-tenant leakage | §5.4 | Covered after the cache fix | "Instance" has two definitions in GA4 (F-05) |
| 5 | JWT forgery | §5.5 | Covered, strongest section | `type` claim enforced while marked `[U]` (F-13) |
| 6 | Token leakage | §5.6 | Covered | Credential scopes exceed the tool surface (F-01, F-02) |
| 7 | SSRF | §5.7 | Covered, surface contradictory | "No URL tool" vs "media import is the URL tool" (F-30) |
| 8 | File upload | §5.8 | Partly covered | XLSX and gzip CSV reintroduce the bomb surface (F-06) |
| 9 | Malicious images | §5.9 | Partly covered | WebP cannot be re-encoded with the stated libraries (F-29) |
| 10 | API abuse | §5.10 | Covered | Cloudflare and GA4 budgets conflict (F-17); GA4 cost function unverified (F-18) |
| 11 | Rate-limit bypass | §5.11 | Covered | Subject-keyed design is correct; no gap found |
| 12 | Task duplication and replay | §5.12 | Covered | Redis write lock has no fencing token; acceptable for v1 |
| 13 | Task state tampering | §5.13 | Covered | State machine and CAS design are sound |
| 14 | Untrusted third-party responses | §5.14 | Partly covered | GraphQL 200-with-`errors` decodes as a zero result (F-25) |
| 15 | Supply chain | §5.15 | Partly covered | No SHA pinning (F-09); release privileges (F-10); SECURITY.md excludes the class (F-22) |

**Threat classes with no threat ID at all:** confirmation approval fatigue (AB-6 covers only self-approval);
property-level authorization in GA4 (not in the RBAC matrix); the R2 and ClickHouse raw-log stores; and
verification of the service-token client secret. These are gaps in the model, not just in the prose.

## 2. Findings

Severity follows the task rubric: Critical is an exploitable design flaw or credential/data exposure; High
is a missing or unenforceable control on a trust boundary; Medium is weak detection or ambiguous policy;
Low is hardening. Two Critical findings present earlier in the review were remediated during it (§6).

| ID | Severity | Location (file:line) | Finding | Attack scenario or impact | Suggested resolution |
|---|---|---|---|---|---|
| F-01 | High | `docs/integrations/cloudflare.md:237`, `docs/integrations/cloudflare.md:246`, `docs/integrations/cloudflare.md:261`, `docs/integrations/cloudflare.md:279`, `docs/security/security-review.md:185` | The "eight independently testable layers" and L6 "token manifest contains read permissions only" coexist with an opt-in profile carrying `Logs: Write`. The document concedes startup cannot detect a silently added write scope. L5's grep gate proves only that the code contains no mutation call, not that the credential cannot mutate. | The guarantee is a code-review property, not a credential property. A refactor, a debug route or a compromised dependency reusing the HTTP client can mutate Cloudflare with the same token, and the "read-only" claim made to users would be false. | Drop "guarantee". State "no mutation code path; the opt-in credential may permit writes". Keep L6 true by scoping it to the default profile, and run the log-inspection profile as a separate process or deployment. |
| F-02 | High | `docs/integrations/magento.md:51`, `docs/security/threat-model.md:32`, `docs/security/security-review.md:177` | The default integration ACL is `Magento_Catalog::products` (which includes delete), `Magento_Sales::sales` and `Magento_Backend::store`. The "read-only deployments use the same role minus write resources" variant is never enumerated, and no startup check verifies the credential's scope (unlike the Cloudflare token verify). | A deployment that believes it is read-only because no write tool is enabled still holds a credential that can delete products and change store configuration. A gateway bug, SSRF or leaked secret bypasses the tool-level guarantee entirely. | Publish an explicit ACL manifest per mode. Probe one write endpoint at startup and require 403. Refuse to start when the credential is broader than the enabled tool set, and mark the claim `[U]` until a live probe exists. |
| F-03 | High | `docs/security/threat-model.md:115`, `docs/security/threat-model.md:452`, `docs/security/threat-model.md:460` | Request path step 3 requires exactly one `Cf-Access-Jwt-Assertion`, but the header table says service tokens use `CF-Access-Client-Id`/`CF-Access-Client-Secret` and are "converted to a subject by the same verifier". No verification of that pair is specified, and service-token credentials are not JWTs. No comparison, storage, rotation or replay rule for the client secret is described. | Two incompatible ingress paths. If the gateway compares secrets itself it becomes a credential verifier with its own timing and rotation bugs; if it forwards to Cloudflare there is an unmodeled network dependency on the auth path, and an unauthenticated path if the origin is reachable. | Specify one machine-identity mechanism. If service tokens are used, define where the pair is validated, how it maps to `sub`, `aud` and instance, and how replay is prevented. Align step 3 with that mechanism. |
| F-04 | High | `docs/security/threat-model.md:294`, `docs/security/threat-model.md:461` | "Token hash held in Redis for the remaining lifetime" plus `401 token_replayed` is single-use semantics with no binding dimension. A normal MCP client sends many requests with one token. | Either every second request fails, or the check is silently weakened to a no-op. Neither is the stated control, and no test can distinguish the two without a binding rule. | Define replay detection as `(token hash, session id)` or `(jti, subject)` with a documented window, and state what a second concurrent request from the same client does. |
| F-05 | High | `docs/integrations/ga4.md:478`, `docs/integrations/ga4.md:488`, `docs/security/threat-model.md:28`, `docs/security/threat-model.md:188` | GA4 defines "instance" as one deployment and admits the GA4 credential is shared, with the caller→property allowlist as "the only thing between caller A and property B". The threat model defines instance as a tenant inside one deployment and forbids shared upstream credentials (T-20). ADR-0014 fixed cross-instance bindings but not property-level grants. | The primary tenancy concept has two meanings. One service-account key with viewer on many properties means any authorization bug, or any tool that accepts a property selector, is a cross-tenant analytics leak. The audit records a property id but the RBAC matrix has no property dimension, so the grant is unmodeled. | Pick one tenancy definition. Make property a first-class grant in the RBAC matrix and audit schema. Use per-property credentials, or document and test a compensating control. |
| F-06 | High | `docs/security/security-review.md:189`, `docs/integrations/magento.md:346`, `docs/integrations/magento.md:358`, `docs/mcp-tools.md:203`, `docs/security/threat-model.md:338` | The review and the Magento design accept normalized XLSX and gzip-compressed CSV. The catalog says XLSX is converted to CSV before validation, but the converter still parses the zip. The threat model claims zip, xlsx and xls are rejected, "which removes the bomb and macro surface entirely". The 8 MiB limit is specified after base64 decode, not after gzip or XLSX expansion. | An XLSX is a zip; a small payload expands to a large sheet or shared-strings table, and macros sit outside the CSV/JSON validator. A gzip CSV is the same bomb through the documented Magento contract. The "surface removed" claim is false. | One format allowlist across all four documents. If XLSX stays, cap decompressed bytes and entry count, reject macros, and apply limits after decompression. Add a bomb test to the T-23/§5.8 matrix. |
| F-07 | High | `docs/security/threat-model.md:512`, `docs/security/threat-model.md:549`, `docs/security/security-review.md:228`, `docs/security/security-review.md:265`, `docs/adr/0012-observability-and-audit.md:61-62`, `docs/adr/0012-observability-and-audit.md:77` | Immutability depends on an optional external sink, an Ed25519 key with no stated custodian, and a verification job inside the same trust domain. Retention is "partition detach and drop", which deletes rows from a hash chain. The audit-failure policy is explicitly undecided in ADR-0012 while the review asserts fail-closed for writes. | An insider with database rights deletes a partition or suppresses the job, and the proof is the attacker's own process. A retention drop makes the chain fail verification, so legitimate retention looks like tampering. The behavior when the audit insert fails at 3 a.m. is undefined. | Decide and document the failure policy. Require a sink for regulated deployments. State key custody and signing location. Make retention checkpoint-aware (archive, then drop) and test a partition drop. |
| F-08 | High | `docs/integrations/cloudflare.md:387`, `docs/integrations/cloudflare.md:395`, `docs/integrations/cloudflare.md:421`, `docs/security/threat-model.md:16`, `docs/security/threat-model.md:47` | The Cloudflare design adds R2 and ClickHouse as stores of raw request logs (IPs, paths, user agents) and an R2 S3-compatible credential. The threat model's scope and asset table stop at PostgreSQL and Redis and list only the Cloudflare API token. | The largest concentration of restricted data in the system sits outside every threat, boundary and control in the model. No classification, retention, access-control or tamper analysis applies to it, and no test covers it. | Add R2 and ClickHouse to scope, assets, boundaries and classification. Add the R2 credential to the secret inventory. Define raw-log retention, access control and deletion, and reference it from TB-6. |
| F-09 | High | `docs/security/threat-model.md:216`, `docs/security/threat-model.md:439`, `.github/workflows/ci.yml:25`, `.github/workflows/security.yml:104`, `.github/workflows/release.yml:58`, `docs/security/security-review.md:73` | All 30 `uses:` references across the four workflows are tag-pinned; zero are 40-character SHAs (verified). T-33 and §5.15 claim SHA pinning and a policy job that fails a tag reference. The review is honest that this is "Planned", the threat model is not. | A moved tag on any write-capable release action (`softprops/action-gh-release@v2`, `sigstore/cosign-installer@v3`, `anchore/sbom-action@v0`) executes attacker code with `contents: write`, `packages: write` and `id-token: write`. | Pin every action to a SHA now, add the lint job that rejects a tag reference, and correct the threat model claim. Treat `@v0` as unacceptable regardless. |
| F-10 | High | `.github/workflows/release.yml:7-10`, `.github/workflows/release.yml:16`, `.github/workflows/release.yml:28`, `.github/workflows/release.yml:52`, `.github/workflows/release.yml:95` | `contents: write`, `packages: write` and `id-token: write` are workflow-scoped, so checkout (with `persist-credentials` defaulting to true), `go test` and `go run github.com/google/go-licenses@v1.6.0` all hold them. The only gate before publishing is `go test ./...`. There is no tag signature check, no verification that the commit passed CI, no `attest-build-provenance`, and no `cosign verify` self-check. | A compromised step or dependency in the release job can push images, edit releases and mint OIDC tokens. T-35's "signed commits, protected tags, provenance attestation" is not implemented by the workflow that publishes. | Per-job minimal permissions and `persist-credentials: false`. Verify the tag and that CI is green for the commit. Run the security gates on the tag. Add attestations, sign `checksums.txt`, and document the verification identity. |
| F-11 | Medium | `docs/security/threat-model.md:491-492`, `docs/security/threat-model.md:483`, `docs/security/threat-model.md:621-627`, `docs/adr/0014-confirmation-approval-and-bindings.md:42-49`, `docs/security/security-review.md:118-139`, `docs/mcp-tools.md:352-356` | The threat model still specifies the superseded confirmation mechanism: a `confirmation_id` computed as a truncated HMAC over a nonce, verified by recomputation, with the human calling `platform.confirm` (an MCP tool). ADR-0014 and the review specify an opaque server-side id approved on a separate human-only surface, 300 s default and 900 s cap, requiring an `approver` permission. ADR-0014 amends ADR-0006, not the threat model, and the threat model's decision record does not list it. The RBAC matrix grants approval to operator/admin without naming the `approver` permission, and nothing caps pending confirmations per subject. | An implementer following the threat model (the document that owns the control and its test matrix) builds the HMAC variant and returns a usable id to the requester, recreating the defect ADR-0014 was written to close. Approval fatigue is unmitigated. | Amend §6.3 and the decision record to point at ADR-0014, add `approver` to the RBAC matrix, delete the HMAC construction, and cap outstanding confirmations per subject and per instance. |
| F-12 | Medium | `docs/mcp-tools.md:135`, `docs/mcp-tools.md:139`, `docs/mcp-tools.md:241` | The catalog defines `admin` as "Inspects gateway or credential metadata; never returned as data" and uses `cloudflare.logs.jobs` as the example, but that tool returns job metadata (id, name, enabled, dataset, last error). Line 139 still says "Cloudflare and GA4 tools are always `read`", contradicting the only admin-scoped Cloudflare tool. | An implementer applying the blanket "always read" rule mis-scopes the tool into viewer/analyst, or maps `admin` scope to "returns nothing" and breaks the tool. The RBAC matrix has no Cloudflare admin row, so the scope has no authorization semantics. | Fix the blanket sentence, define `admin` as "metadata only, never upstream content", and add the tool to the RBAC matrix and its test. |
| F-13 | Medium | `docs/security/threat-model.md:460`, `docs/security/threat-model.md:454`, `docs/adr/0006-authentication.md:36`, `docs/adr/0006-authentication.md:67` | The `type` claim separates human from service identity but is tagged `[U]`, and behavior when it is absent is unspecified. ADR-0006 allows RS256 and ES256 while the threat model requires a single allowlisted algorithm. ADR-0006 promises a documented OIDC fallback for self-hosters; the review says it is planned. | If live tokens omit `type`, either every human is rejected or identity classes collapse, which is the confusion ADR-0006 exists to prevent. The algorithm mismatch means the startup assertion either rejects a valid EC key set or permits two algorithms. | Confirm the claim against a live token before Phase 1. State the fail-closed behavior for an absent `type`. Align the algorithm set. Ship the OIDC document or remove the promise. |
| F-14 | Medium | `docs/mcp-tools.md:309`, `docs/mcp-tools.md:341`, `docs/mcp-tools.md:342`, `docs/mcp-tools.md:73`, `docs/security/threat-model.md:249` | `magento.configurable.create` declares `instance` as a bare string, `children[].price` as a bare string, and `children[].attributes` as `additionalProperties: {type: string}`, an arbitrary map keyed by attribute code. This contradicts "additionalProperties: false at every level" and the claim that a CI test rejects unconstrained strings. | An agent injects arbitrary attribute codes into an EAV write, bypassing the "identifiers are constrained by pattern" rule. The attribute map is the natural injection point for a crafted `custom_attributes` payload. | Use `propertyNames` with the attribute-code pattern or enumerate allowed codes. Constrain price and instance. Add the schema-lint test the documents claim exists. |
| F-15 | Medium | `docs/mcp-tools.md:74`, `docs/mcp-tools.md:291`, `docs/security/threat-model.md:249`, `docs/integrations/magento.md:266` | Three SKU grammars are in force: `[A-Za-z0-9._:-]{1,64}`, `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` and `[A-Za-z0-9._-]{1,64}`. The schema allows a leading `:` or `.` and a colon anywhere; the validator requires a leading alphanumeric and forbids `:`. Attribute codes also differ (`[a-z0-9_]{1,64}` vs `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`). | A SKU accepted by the schema and rejected by the module produces inconsistent errors, and which layer is authoritative is undefined. Identifier grammars are the injection boundary for URL construction. | One shared grammar per identifier type, generated into both the JSON Schema and the Go validator from a single source, with a contract test. |
| F-16 | Medium | `docs/integrations/magento.md:266`, `docs/integrations/magento.md:421`, `docs/integrations/magento.md:424`, `docs/security/threat-model.md:279`, `docs/security/threat-model.md:510` | The Magento error table echoes the requested SKU on 404, returns `sku already exists (PHONE-01-BLK-128)`, and names the missing ACL resource on 403. The threat model requires a fixed code set and no upstream detail. | A SKU existence oracle across the catalog, useful for enumeration and for confirming a competitor's SKU. Disclosure of ACL resource names to an agent. Not cross-tenant, but it violates the stated error contract. | Return `not_found` and `conflict` without the value. Log the value with the correlation id. Keep ACL names server-side. |
| F-17 | Medium | `docs/security/threat-model.md:198`, `docs/security/threat-model.md:524`, `docs/security/threat-model.md:525`, `docs/integrations/cloudflare.md:183`, `docs/integrations/ga4.md:387` | The Cloudflare budget is 60 per 5 minutes in the threat model and 150 per 5 minutes in the integration design. The GA4 budget is an instance-level 25% reserve in the threat model and an equal per-property split in the GA4 design. | The limiter cannot implement both. On a multi-property deployment the two rules disagree about whether a property may consume the reserve, so either humans are starved or the reserve is unenforced. | One budget model with a stated precedence (strictest wins), derived from one config source, with a multi-property test. |
| F-18 | Medium | `docs/integrations/ga4.md:369`, `docs/integrations/ga4.md:374`, `docs/security/threat-model.md:367` | The GA4 cost function is unpublished and unverified. The gateway estimates `rows × columns × cardinality_factor` and claims rejection before the call. The only measurement is about 10 tokens per small report; the worst case is unknown. | An estimate that under-counts lets a caller exhaust the 200,000-token daily quota. T-25's "rejected before the upstream call" is only as good as an unvalidated heuristic, so the control cannot be tested as written. | Calibrate the cost function across shapes, store the coefficients, and assert the estimate is within a stated error bound. Otherwise describe it as a heuristic, not a control. |
| F-19 | Medium | `docs/integrations/ga4.md:197`, `docs/integrations/ga4.md:408` | Metadata is cached for the process lifetime and every compatibility failure is cached "until restart", with no bound. | An agent generates unlimited distinct invalid field pairs or touches many properties, growing the cache without limit and burning the 120 potentially-thresholded-requests-per-hour bucket. A long-lived process becomes an OOM candidate. | Bound both caches (LRU, maximum entries) and rate-limit validator-miss 400s per subject. |
| F-20 | Medium | `docs/security/threat-model.md:144` | Unmasking is a request argument (`pii_unmask: true`) in the tool schema, gated by role and audited after the fact. | An injected instruction tells the agent to set the flag. The only barrier is the role grant, so any analyst-level agent can be induced to unmask every record it reads and repeat it into the conversation, where the gateway has no control (R-7). | Make unmasking a separate tool with a per-call confirmation, or a per-session setting an operator enables. Cap unmasked rows and alert on volume. |
| F-21 | Medium | `docs/security/threat-model.md:508`, `docs/security/threat-model.md:510` | `params_hash` is JCS plus unsalted SHA-256 over arguments that include emails, phones, order increment ids and SKUs. Only the subject uses a pepper. | The audit table becomes a dictionary-attackable index of restricted values: hash a candidate email list, join to the audit table, and learn who was queried and when. That is the "customer identifiers beyond a keyed hash" promise applied to the wrong field. | Key `params_hash` with the same server pepper or a per-deployment key. Keep an unkeyed hash only where cross-deployment reproducibility is genuinely required. |
| F-22 | Medium | `SECURITY.md:92`, `SECURITY.md:150`, `SECURITY.md:188`, `docs/security/threat-model.md:20`, `docs/security/threat-model.md:430` | Triage is 7 business days in SECURITY.md and 10 in the threat model. Build-time dependency compromise is out of scope in SECURITY.md while supply chain is a Critical mandatory class. Signing is claimed from v0.1.0, but the release signs blobs only, image signing is conditional on a Dockerfile that does not exist, and no verification command or signing identity is documented. | A reporter of the class the design rates Critical is told it is out of scope. Users cannot verify a signature without the expected identity. The response SLA is ambiguous. | Align the SLA. Carve gateway-reachable dependency compromise back in. Document `cosign verify-blob` with the certificate identity and OIDC issuer, and sign the checksum file. |
| F-23 | Medium | `docs/security/threat-model.md:621-627`, `docs/adr/0006-authentication.md:3`, `docs/security/security-review.md:36`, `CODEOWNERS:12-16` | The threat model's decision record cites ADR-0001 to ADR-0005 with titles that do not match the ADRs on disk (ADR-0001 is modular architecture, ADR-0004 is Cloudflare log storage) and marks them Pending, while ADR-0006 and ADR-0014 are Accepted and unlisted. Control locations differ across documents: `internal/auth/jwt.go` vs `internal/security/jwt` vs CODEOWNERS `/internal/security/`; `internal/modules/magento` vs `/internal/magento`. | "Every control has an implementation location and a test" is untraceable. A reviewer cannot tell which ADR governs authentication, and the CODEOWNERS path for security review may point at a package that never exists. | Renumber to the ADRs on disk and list every Accepted ADR. Fix one package layout across the threat model, the review and CODEOWNERS. |
| F-24 | Medium | `docs/development/research-baseline.md:19`, `docs/development/research-baseline.md:22`, `docs/integrations/ga4.md:318-319`, `docs/integrations/ga4.md:507`, `docs/mcp-tools.md:203`, `docs/mcp-tools.md:210` | Baseline rule 2 forbids presenting `[U]` claims as working features, and rule 4 forbids customer data in documents. The tool catalog carries no tags at all yet freezes import and XLSX tools whose upstream availability is `[U]` or Adobe-only. The GA4 design publishes a live property id, account id, display name and business metrics (370,599 sessions, 348,529 users) for a real property. | The catalog becomes a contract for capabilities that may not exist. The GA4 document leaks commercial intelligence about a real property into a public repository, which is the category the baseline says must not appear. | Add tags to the catalog or state the exemption explicitly. Replace property and account identifiers and absolute volumes with placeholders or ratios in public documents. |
| F-25 | Medium | `docs/integrations/cloudflare.md:167`, `docs/security/threat-model.md:424` | Cloudflare GraphQL returns HTTP 200 with an `errors` array for query failures, but the untrusted-response control decodes only 2xx with an allowlisted content type and never mentions the `errors` array. | A rate-limited or invalid query decodes as a successful empty result, producing "no traffic" or "no attacks" — the plausible-but-wrong zero T-23 exists to prevent, indistinguishable from a real zero. | Treat a non-empty `errors` array as a typed upstream error regardless of status, and add it to the T-23 test matrix. |
| F-26 | Medium | `docs/integrations/magento.md:64-67`, `docs/security/threat-model.md:504`, `docs/security/security-review.md:263` | The Magento base URL allows plain `http` for localhost and 127.0.0.1 in dev, while the threat model says https and port 443 only and the review says plaintext HTTP is rejected. | A dev config that reaches production gives cleartext admin tokens on the wire and an SSRF pivot to localhost services, including the Magento admin or a local metadata proxy. | Make the exception compile-time (dev build tag) or remove it. If it stays, require an explicit logged flag and refuse it when any write tool is enabled. |
| F-27 | Medium | `docs/security/threat-model.md:482`, `docs/security/threat-model.md:485`, `docs/security/threat-model.md:470-484` | The matrix denies admin order and customer reads to enforce separation of duties, but admin has "Instance and role configuration: yes" and nothing requires a second approver for role-map changes. | An admin grants itself `analyst` plus `pii:read`, or maps its own group to operator, and the separation-of-duties property disappears. The audit records the change; no control prevents it. | Role-map changes require a second admin or an out-of-band approval, are audited as a security event, and alert on any grant to the actor's own subject. |
| F-28 | Medium | `docs/security/threat-model.md:197`, `docs/security/threat-model.md:235`, `docs/security/threat-model.md:537`, `docs/integrations/magento.md:252-257`, `docs/mcp-tools.md:276` | T-24's structural control "no tool both consumes untrusted content and performs a write" is false for `magento.product.update` (reads the product, sends the full extension block back), import (reads CSV, writes) and `unified.mapping.sku_propose` (reads GA4 and Magento, writes). Write confirmation now reduces the impact, but the read→write chain inside one call remains. | An injected product name shapes the parameters of a write that a human is then asked to approve; the approval diff is derived from attacker-influenced content, so the human approves a summarised version of the injection. | State the invariant per call rather than per tool, require explicit write parameters instead of read-modify-write where possible, and mark untrusted provenance in the approval diff. |
| F-29 | Medium | `docs/security/threat-model.md:352`, `docs/security/threat-model.md:526`, `docs/integrations/magento.md:275` | The image pipeline allows WebP input and requires decode-and-re-encode using the standard library plus `golang.org/x/image`, whose WebP package is decode-only. Magento's validator rejects `image/webp`. The 40 MP cap times the 64 in-flight cap is about 10 GB of decoded pixels. | The re-encode control cannot be implemented for one of the four allowed formats as specified, and the documented concurrency cap makes the memory bound unbounded in practice. | Drop WebP or add a reviewed encoder. Bound decoded bytes globally with a semaphore rather than per request. Add a concurrent-decode memory test. |
| F-30 | Medium | `docs/security/threat-model.md:199`, `docs/security/threat-model.md:323`, `docs/security/threat-model.md:233`, `docs/security/security-review.md:167`, `docs/integrations/magento.md:308` | Three documents say no tool accepts a URL. T-26 says the only URL-accepting tool is media import. The §5.1 and §5.7 attack paths name `magento.product.media.add_from_url`, which does not exist in the catalog (media_upload takes base64). | The SSRF corpus is specified against a tool that either does not exist or contradicts the "no URLs" guarantee. If it exists the guarantee is false; if it does not, the test proves nothing about the real surface. | State the surface once, name the real tool, and point the corpus at it. Remove the fictional tool from the attack paths. |
| F-31 | Low | `docs/security/threat-model.md:189` | The tool-description control says the registry rejects any tool whose description differs from the compiled hash, but the description is a compiled constant, so the comparison is against itself. The startup hash log detects nothing at runtime. | A slogan control. The underlying property (static descriptions) is good, but the claimed enforcement and detection do not exist, and the test cannot fail for the reason stated. | Describe it as "descriptions are compile-time constants with no runtime input path", and test that the registry API cannot accept a description from configuration. |
| F-32 | Low | `.github/workflows/ci.yml:53`, `.github/workflows/ci.yml:90`, `.github/workflows/security.yml:8`, `.github/workflows/security.yml:54`, `.github/workflows/security.yml:68` | The Go jobs and every Go scanner are gated on `go.mod` existing, so today they are skipped and report success. `govulncheck` runs weekly, not nightly as claimed. DCO runs only on `pull_request` and there is no `merge_group` trigger. | Branch protection cannot distinguish "passed" from "skipped". A merge-queue or direct push bypasses DCO and every Go check. The nightly claim is false. | Fail closed when Go files exist, add `merge_group`, and align the schedule with the claim. |
| F-33 | Low | `docs/security/threat-model.md:30`, `docs/security/threat-model.md:174`, `docs/security/threat-model.md:175` | A3 says the session is stateless except for an ID, but T-11 stores a Redis session record and T-12 keeps a per-session resumability buffer signed with a session key. | The stateless assumption is used to argue there is no server-side state to protect, when two state stores exist with their own tampering and expiry behavior that the model does not analyze. | Delete A3 or restate it as "no conversation state", and add the session record and event buffer to the TB-6 assets. |

## 3. The three most serious findings

**F-02: the read-only Magento claim rests on an unenumerated ACL.** The document says orders and customers
are read-only and that read-only deployments use the same role "minus write resources", but the default
role it lists includes product delete and store configuration, and nothing at startup verifies what the
credential can actually do. Unlike Cloudflare, there is no verify endpoint in the design. The tool surface
is a promise; the credential is the capability, and they disagree.

**F-05: one GA4 credential spans tenants.** The GA4 design states plainly that the credential is shared and
the property allowlist is the only barrier between caller A and property B. ADR-0014 fixed multi-instance
authorization for `unified.*` tools with bindings, but a property is not a binding, the RBAC matrix has no
property dimension, and the allowlist lives in the bearer-token layer rather than the policy engine. A
single mistake in that mapping is cross-tenant analytics disclosure.

**F-09: the supply-chain control is asserted, not implemented.** The threat model says every Action is
pinned to a commit SHA and that a policy job rejects tag references. All 30 references are tags. The release
workflow — the one that holds `contents: write`, `packages: write` and `id-token: write` — uses those tags.
This is the gap between a document that describes the control and a repository that does not have it.

## 4. Controls that are well designed

These are genuinely solid and should be preserved, not rewritten:

- **Confirmation, as now specified in ADR-0014** (`ADR-0014:42-49`, `security-review.md:118-139`). Opaque
  id, separate human-only approval surface, `approver_hash <> requester_hash`, parameter-hash binding,
  single use, 300 s default and 900 s cap, fail closed when the surface is unavailable. This closes the
  self-approval path correctly; the remaining defect is that the threat model was not amended (F-11).
- **JWT verification design** (`threat-model.md:294`, `threat-model.md:449-464`). Algorithm allowlist with
  `none` and HMAC rejected before signature verification, `kid` as a lookup key only, `jku`/`x5u`/`jwk`
  ignored, redirects disabled, 3 s fetch timeout, 8 KiB token cap, exact `iss`/`aud`, 60 s skew and an
  `exp - iat` cap. This is a correct application of RFC 8725 and the strongest section of the set.
- **JWKS refresh budget** (`threat-model.md:168`). Single-flight, 10-minute cache, one refetch per 60 s,
  30 s negative cache, 1-hour stale-while-error, with a 1,000-random-`kid` test. Concrete and testable.
- **Cache authorization** (`threat-model.md:279`). The key now carries `authz_scope_hash`, responses are
  shaped before caching and never shared across scopes, and the value is re-checked on read. This is the
  correct fix for a defect that was Critical earlier in the review.
- **Egress design** (`threat-model.md:323`, `threat-model.md:504`). Resolve once, validate every address,
  dial the validated IP with `ServerName` set, normalise 4-in-6, refuse cross-host redirects, no proxy from
  the environment, validate instance base URLs at startup. The TTL-0 DNS-rebinding harness
  (`threat-model.md:325`) is a real test rather than a claim.
- **Strict JSON pre-parse** (`threat-model.md:249`). Duplicate keys, prototype keys, `UseNumber`, depth and
  array caps, 1 MiB `MaxBytesReader`, fuzz target. It correctly states that schema validation is structural
  and not semantic.
- **Rate-limit keying** (`threat-model.md:381`). Keyed on the verified subject, not the IP or the session,
  atomic Lua across replicas, strictest of subject/instance/deployment. Session rotation is irrelevant by
  construction, which is the right property.
- **Job state machine** (`threat-model.md:409`). Compare-and-set transitions, no client-writable status,
  cancellation re-read from the database, no `DELETE` for the runtime role.
- **Secret type** (`threat-model.md:308`). `String`, `GoString`, `MarshalJSON` and `Format` all redact,
  `Reveal` is lint-checked, and a canary test sweeps logs, panics and metrics. This is how a secret type
  should be built.
- **Cloudflare query construction** (`cloudflare.md:241-248`) and the corrected log tool
  (`mcp-tools.md:241`). Closed route table, no caller-controlled method or path, templates rejected at build
  time if they contain a mutation or subscription root, and the log-inspection tool now returns metadata
  only and excludes destination configuration. The credential claim (F-01) is separate from the code design,
  which is sound.
- **GA4 compatibility validator** (`ga4.md:196-198`, `ga4.md:241-247`). Three layers, observation dates on
  every rule, and a documented correction of its own baseline using live evidence. The best epistemic
  hygiene in the document set.
- **Magento write discipline** (`magento.md:29-31`, `magento.md:204-212`, `magento.md:252-257`,
  `magento.md:454-456`). "HTTP 200 is not correctness", per-transition idempotency keys, resumable state,
  read-then-send of the full extension block, and no automatic retry on a write timeout with read-back by
  natural key.
- **Data-quality semantics** (`cloudflare.md:499-509`, `ga4.md:430-440`). `bot_score` is null and never 0,
  `absent` is distinguished from `unknown`, sampling and staleness are labelled. This prevents the most
  likely real-world error in the whole product.
- **Candour about limits** (`cloudflare.md:279`, `SECURITY.md:15-18`, `security-review.md:5`). The
  documents say what they cannot do. Most of the findings above are places where another document did not
  match that standard.

## 5. Claims checked and found unsupported

| Claim | Where | What the repository actually shows |
|---|---|---|
| Actions pinned to full commit SHAs | `threat-model.md:216`, `threat-model.md:439` | 0 of 30 `uses:` references are SHAs; all are tags. The review admits this is planned (`security-review.md:73`). |
| Image provenance attestation | `threat-model.md:439` | No attestation step in `release.yml`; only buildx `provenance: true`, and only when a Dockerfile exists. |
| `govulncheck` nightly | `threat-model.md:439` | Weekly cron (`security.yml:8`). |
| No write or exfiltration path without permission | `docs/reviews/phase-0-report.md:139` | Writes now require confirmation, but `product.update` writes free-form text and the read→write chain inside one call remains (F-28). |
| Audit trail is append-only and immutable | `threat-model.md:512` | Sink optional, key custody unstated, partition drop, failure policy undecided. |
| PostgreSQL never sees a raw log record | `cloudflare.md:387` | Narrowly true; raw logs live in R2 and ClickHouse, outside the threat model entirely. |
| Cloudflare read-only guarantee | `cloudflare.md:237` | The opt-in profile carries `Logs: Write`; L6 cannot hold for it. |
| No tool accepts a URL | `threat-model.md:323`, `security-review.md:167` | T-26 and the §5.1 attack path name a URL-accepting media tool. |
| Magento and GA4 credentials are read-only by default | `security-review.md:177` | The Magento ACL includes product delete and store configuration; the GA4 credential is shared across properties. |
| All fifteen mandatory threat classes are covered | `docs/reviews/phase-0-report.md:133` | Every class has a section; four lack a threat ID for their real failure mode (see §1). |

## 6. Changes observed during the review

The documents were edited while this review was in progress. Two findings that were Critical in the 00:17
snapshot are no longer valid, and were re-verified rather than carried forward:

1. `cloudflare.logs.jobs` was `read` scope and returned Logpush destination credentials. At 00:26 it became
   `admin` scope and the destination configuration is excluded (`mcp-tools.md:135`, `mcp-tools.md:241`).
   The residual issue is the catalog's own definition and blanket rule (F-12), and the credential profile
   (F-01).
2. `magento.md` specified `confirm=true` as the write gate. At 00:26 it was replaced with the two-step
   protocol and now cites ADR-0014 (`magento.md:277`, `magento.md:490`). ADR-0014 was added and resolves
   the self-approval and approval-surface defects. The residual issue is that the threat model still
   describes the superseded mechanism (F-11).
3. The cache key gained `authz_scope_hash` and "shaped before caching" (`threat-model.md:279`), closing the
   cross-privilege cache finding.
4. `mcp-tools.md` now requires confirmation for `write` by default (`mcp-tools.md:133`), closing the
   unconfirmed-write finding for the catalog. The remaining capability-separation gap is F-28.

This is the right direction. It also means any review of an earlier commit will see Criticals that no
longer exist; the digests above pin what was assessed.

## 7. Verdict

**Approve with changes.**

No Critical finding remains at the pinned snapshot. Ten High findings are conditions on the Phase 1 gate:
**F-01 through F-10**. Two of them are documentation-vs-reality gaps that an implementer will otherwise
build to the wrong specification and should be fixed before code starts: **F-11** (threat model still
specifies the superseded confirmation mechanism) and **F-12** (catalog scope contradiction). The rest can
be resolved during implementation, with F-13 and F-15 fixed before the first authentication and identifier
code lands.

Required before Phase 1 exits:

1. An explicit Magento ACL manifest per mode and a startup probe, or the read-only claim downgraded to `[U]`.
2. A decision on GA4 property authorization: a property grant in the RBAC matrix and audit schema, or
   per-property credentials.
3. One confirmation specification, with the threat model amended to ADR-0014 and `approver` added to the
   matrix.
4. One format allowlist across the threat model, review, catalog and Magento design, with post-decompression
   limits.
5. A decided audit failure policy, a required sink for regulated deployments, and checkpoint-aware retention.
6. R2 and ClickHouse added to the threat model's scope, assets and classification.
7. SHA pinning in all four workflows and minimal per-job release permissions.
8. One SKU and attribute-code grammar shared by schema and validator.

Per-document disposition: threat model — keep, amend §6.3 and the ADR record, correct the SHA-pinning and
capability-separation claims; security review — keep, adopt its confirmation protocol as normative; ADR-0006
— sound, confirm the `type` claim and the algorithm set; ADR-0012 — acceptable only after it makes the
decision it was written to make; ADR-0014 — good, make it the single confirmation reference; SECURITY.md —
good disclosure process, wrong scope carve-out and ambiguous SLA; workflows — no SHA pinning and an
over-privileged release; tool catalog — fix the `admin` definition and the unconstrained schema fields;
integration designs — Magento's ACL claim is unverified, Cloudflare's read-only guarantee is overstated,
GA4 publishes a real tenant's data.

## 8. Limitations of this review

No implementation exists, so this review assesses design intent and documentation consistency, not runtime
behavior. No live Magento, Cloudflare or GA4 call was made; vendor behavior claims are taken from the
baseline and the evidence files, not re-verified. Git history, branch protection, environment reviewers and
repository settings were not inspected (no git commands were run), so the CI findings are limited to what
the workflow files show. Findings do not assert vulnerabilities in Magento, Cloudflare or Google products.
