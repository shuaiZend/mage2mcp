# Lint debt inventory

`golangci-lint run` reports these on `main` at v0.11.8. None were introduced by that release;
they accumulate because `local-ci.sh` is the only gate that runs the linter, and it had been
failing earlier for an unrelated reason (the skip gate counted packages), so nobody reached
this stage.

**Correction.** The list below was first recorded as 73 findings. That was wrong: it was taken
with golangci-lint's defaults, which cap repeats at `max-same-issues: 3` and stop a linter at
`max-issues-per-linter: 50`. Measured with both caps off at the same commit, the real count is
**101**. One directory reported 17 and actually had 27. The lesson is the same one this project
kept relearning: a tool's output is not the whole truth unless you ask what it left out.

Total: **101** findings (73 as first measured, truncated by the default caps).

| Directory | Count |
|---|---:|
| `internal/storage` | 24 |
| `internal/magento` | 18 |
| `scripts/testinventory` | 11 |
| `internal/mcp` | 10 |
| `scripts/gen-prompts` | 2 |
| `scripts/toolinventory` | 2 |
| `internal/security` | 2 |
| `scripts/tool-contract` | 1 |
| `cmd/admin` | 1 |
| `scripts/gen-implemented-tools` | 1 |
| `internal/jobs` | 1 |

## Findings

```text
internal/mcp/server/server_test.go:957:21: Error return value of `session.Close` is not checked (errcheck)
scripts/testinventory/inventory.go:210:13: Error return value of `fmt.Fprintf` is not checked (errcheck)
scripts/testinventory/inventory.go:212:13: Error return value of `fmt.Fprintf` is not checked (errcheck)
scripts/testinventory/inventory.go:220:15: Error return value of `fmt.Fprintln` is not checked (errcheck)
scripts/testinventory/inventory.go:222:15: Error return value of `fmt.Fprintf` is not checked (errcheck)
scripts/testinventory/inventory.go:228:15: Error return value of `fmt.Fprintln` is not checked (errcheck)
scripts/testinventory/inventory.go:229:15: Error return value of `fmt.Fprintln` is not checked (errcheck)
scripts/testinventory/run.go:85:14: Error return value of `fmt.Fprint` is not checked (errcheck)
scripts/tool-contract/write_disclosure_test.go:104:21: Error return value of `session.Close` is not checked (errcheck)
internal/storage/operation_gate_test.go:269:6: comparing with == will fail on wrapped errors. Use errors.Is to check for a specific error (errorlint)
scripts/testinventory/inventory.go:256:21: type assertion on error will fail on wrapped errors. Use errors.As to check for specific errors (errorlint)
scripts/testinventory/inventory.go:260:36: non-wrapping format verb for fmt.Errorf. Use `%w` to format errors (errorlint)
scripts/testinventory/run.go:82:6: comparing with == will fail on wrapped errors. Use errors.Is to check for a specific error (errorlint)
cmd/admin/main.go:13:1: File is not properly formatted (goimports)
internal/magento/live_defects_test.go:10:1: File is not properly formatted (goimports)
scripts/gen-implemented-tools/main.go:30:12: G306: Expect WriteFile permissions to be 0600 or less (gosec)
scripts/gen-prompts/main.go:109:15: G304: Potential file inclusion via variable (gosec)
scripts/gen-prompts/main.go:170:12: G306: Expect WriteFile permissions to be 0600 or less (gosec)
scripts/testinventory/inventory.go:251:9: G204: Subprocess launched with variable (gosec)
scripts/toolinventory/inventory.go:314:15: G304: Potential file inclusion via variable (gosec)
internal/magento/client.go:274:2: ineffectual assignment to message (ineffassign)
internal/storage/operation_ledger.go:1315:3: ineffectual assignment to reason (ineffassign)
internal/storage/operation_ledger_test.go:646:2: ineffectual assignment to store (ineffassign)
internal/jobs/jobs.go:44:1: exported: comment on exported var ErrLeaseLost should be of the form "ErrLeaseLost ..." (revive)
internal/magento/client.go:840:2: exported: comment on exported const ObservationApplied should be of the form "ObservationApplied ..." (revive)
internal/magento/client.go:842:2: exported: comment on exported const ObservationNotApplied should be of the form "ObservationNotApplied ..." (revive)
internal/magento/client.go:844:2: exported: comment on exported const ObservationInconclusive should be of the form "ObservationInconclusive ..." (revive)
internal/magento/import.go:116:34: unused-parameter: parameter 'ctx' seems to be unused, consider removing or renaming it as _ (revive)
internal/magento/import_test.go:602:2: redefines-builtin-id: redefinition of the built-in function cap (revive)
internal/magento/live_defects_test.go:307:49: unused-parameter: parameter 'w' seems to be unused, consider removing or renaming it as _ (revive)
internal/magento/live_defects_test.go:1044:45: unused-parameter: parameter 't' seems to be unused, consider removing or renaming it as _ (revive)
internal/magento/media_upload_test.go:73:72: unused-parameter: parameter 'r' seems to be unused, consider removing or renaming it as _ (revive)
internal/magento/media_upload_test.go:94:73: unused-parameter: parameter 'r' seems to be unused, consider removing or renaming it as _ (revive)
internal/magento/operations.go:437:1: exported: comment on exported method Operations.Get should be of the form "Get ..." (revive)
internal/magento/operations.go:445:1: exported: comment on exported method Operations.List should be of the form "List ..." (revive)
internal/magento/operations.go:453:1: exported: comment on exported method Operations.Dispose should be of the form "Dispose ..." (revive)
internal/mcp/server/server.go:427:34: context-as-argument: context.Context should be the first parameter of a function (revive)
internal/mcp/server/server_test.go:1866:17: unused-parameter: parameter 'next' seems to be unused, consider removing or renaming it as _ (revive)
internal/mcp/server/server_test.go:1867:55: unused-parameter: parameter 'r' seems to be unused, consider removing or renaming it as _ (revive)
internal/mcp/toolerr/toolerr.go:81:2: redefines-builtin-id: redefinition of the built-in function max (revive)
internal/security/confirm/confirm.go:328:30: redefines-builtin-id: redefinition of the built-in function max (revive)
internal/storage/jobqueue.go:377:1: exported: comment on exported method Store.PruneJobs should be of the form "PruneJobs ..." (revive)
internal/storage/jobqueue.go:401:1: exported: exported method JobQueue.Cancel should have comment or be unexported (revive)
internal/storage/operation_ledger.go:92:2: exported: comment on exported const DecisionProceed should be of the form "DecisionProceed ..." (revive)
internal/storage/operation_ledger.go:95:2: exported: comment on exported const DecisionRetry should be of the form "DecisionRetry ..." (revive)
internal/storage/operation_ledger.go:98:2: exported: comment on exported const DecisionInProgress should be of the form "DecisionInProgress ..." (revive)
internal/storage/operation_ledger.go:101:2: exported: comment on exported const DecisionReplay should be of the form "DecisionReplay ..." (revive)
internal/storage/operation_ledger.go:104:2: exported: comment on exported const DecisionUnknown should be of the form "DecisionUnknown ..." (revive)
internal/storage/operation_ledger.go:190:2: exported: comment on exported const ResolvedSucceeded should be of the form "ResolvedSucceeded ..." (revive)
internal/storage/operation_ledger.go:192:2: exported: comment on exported const ResolvedFailed should be of the form "ResolvedFailed ..." (revive)
internal/storage/operation_ledger.go:194:2: exported: comment on exported const ResolvedUnknown should be of the form "ResolvedUnknown ..." (revive)
internal/storage/operation_ledger.go:1080:2: exported: comment on exported const GateProceed should be of the form "GateProceed ..." (revive)
internal/storage/operation_ledger.go:1082:2: exported: comment on exported const GateRetry should be of the form "GateRetry ..." (revive)
internal/storage/operation_ledger.go:1085:2: exported: comment on exported const GateInProgress should be of the form "GateInProgress ..." (revive)
internal/storage/operation_ledger.go:1087:2: exported: comment on exported const GateReplay should be of the form "GateReplay ..." (revive)
internal/storage/operation_ledger.go:1089:2: exported: comment on exported const GateUnknown should be of the form "GateUnknown ..." (revive)
internal/storage/operation_ledger.go:1356:2: exported: comment on exported const DisposedApplied should be of the form "DisposedApplied ..." (revive)
internal/storage/operation_ledger.go:1359:2: exported: comment on exported const DisposedNotApplied should be of the form "DisposedNotApplied ..." (revive)
internal/storage/operation_ledger.go:1362:2: exported: comment on exported const DisposedRequeue should be of the form "DisposedRequeue ..." (revive)
internal/storage/operation_ledger_test.go:81:57: context-as-argument: context.Context should be the first parameter of a function (revive)
internal/storage/operation_ledger_test.go:990:75: context-as-argument: context.Context should be the first parameter of a function (revive)
scripts/toolinventory/inventory.go:3:1: package-comments: package comment should be of the form "Package toolinventory ..." (revive)
internal/magento/live_defects_test.go:72:3: QF1002: could use tagged switch on r.Method (staticcheck)
internal/security/rbac/rbac.go:210:3: QF1002: could use tagged switch on id (staticcheck)
internal/storage/jobqueue_test.go:66:5: QF1001: could apply De Morgan's law (staticcheck)
internal/mcp/server/server.go:805:28: unnecessary conversion (unconvert)
internal/magento/catalog.go:268:96: (*Service).hydrateCategoryProducts - result 1 (error) is always nil (unparam)
internal/magento/client.go:555:34: decodeUpstreamBusinessError - method always receives http.MethodPost ("POST") (unparam)
internal/magento/order.go:151:64: (*Order).fillShippingAddressFromAssignments - result 0 (error) is always nil (unparam)
internal/mcp/server/server.go:1253:56: replayResult - result 1 (error) is always nil (unparam)
internal/mcp/server/server_test.go:1266:6: func gateServer is unused (unused)
internal/mcp/server/server_test.go:3127:6: func withoutField is unused (unused)
internal/mcp/server/server_test.go:3139:6: func outputDescriptionContaining is unused (unused)
```
