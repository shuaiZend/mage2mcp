# MCP-08: the output envelope, and whether it is worth 106 schemas

## What is missing

Every output property now carries a description, enforced at registration. What the
finding also asked for is a **common envelope**: every tool result stating which
instance it came from, a request id for correlating with the server log, and — where it
applies — the window or the truncation state.

Today a caller can only learn the instance from the tool it called, and cannot correlate
a result with a log line at all.

## Options

| # | Design | Cost | Risk |
|---|---|---|---|
| **A** | Wrap every output in `{"instance": "...", "request_id": "...", "data": {...}}` | All 106 output schemas change shape; every existing client breaks | High. This is a major-version change |
| **B** | Add `instance` and `request_id` as optional top-level fields on every output type | 106 structs gain two fields; additive, so no client breaks | Medium. Mechanical, but it touches every output type and every output test |
| **C** | Return the correlation in a protocol-level place instead: an MCP `_meta` field on the tool result | One place in the middleware; no schema change | Low, and it is where the protocol puts out-of-band result metadata. `instance` is already in the audit row and the log |
| **D** | Record the gap and leave it | None | A caller keeps correlating by timestamp |

## Recommendation

**C.** The finding's substance is that a result cannot be correlated with its log line.
MCP already has a field for result metadata, so the fix does not need to touch a single
output schema. `instance` and `request_id` go into `_meta` on every tool result, set in
the one place the middleware already handles results.

A and B would make the envelope part of the payload, which is the design the finding
imagined — but it is also the design that changes 106 schemas, and the benefit over
`_meta` is marginal.

## If you choose C

The work is: set `_meta` in the middleware's result path, a test asserting every tool
result carries both fields, and a line in the tool documentation.
