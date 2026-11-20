# PERF-16: the configurable path, and what parallelising it would change

## What is already done

Bulk product updates and bulk attribute-option creation now fan out under a bound of 8,
with per-row outcomes and request order preserved. The remaining sequential paths are
the configurable children/variations workflow and Magento's async bulk endpoints.

## Why the configurable path was left alone

`CreateConfigurable` creates the parent, then each child, then each child's link, then
the stock. The children stage **fails fast**: the first child error ends the workflow and
returns that one SKU, with `completed_stages` and `orphan_skus` describing what already
exists so the caller can compensate.

Running the children concurrently would mean several can fail at once, and the result
contract has room for exactly one `failed_stage` and one failed SKU. "Which child
failed" would become nondeterministic, which is worse than slow.

## Options

| # | Design | Cost | Risk |
|---|---|---|---|
| **A** | Leave it sequential; keep the fail-fast contract | None | A 50-child create stays at ~152 requests (~12 s at 80 ms RTT), inside the 60 s tool budget |
| **B** | Parallelise children, change the error contract to carry a list of failures with their SKUs | New `failed_children` field, new compensation semantics, and every existing test of the partial path changes | Medium-high. The compensation story is the reason `orphan_skus` exists; making failures plural means the caller must reason about several orphans at once |
| **C** | Parallelise only the **link** stage (children stay sequential) | One stage | **Not actually low risk.** This option was recommended in an earlier revision on the reading that links are independent. Re-reading the code: the link stage also fails fast and reports one `failedLink`, and it follows the links with a verification read that the whole stage depends on. Parallelising it makes "which link failed" nondeterministic in exactly the same way as the children stage, for a smaller saving |
| **D** | Use Magento's async bulk endpoints where available | Feature detection per store, a different result model (poll instead of respond), and the ledger's "one call, one operation" premise no longer holds | High. This is a redesign of the write path, not an optimisation |

## Recommendation

**A, unless the failure contract is on the table.** The earlier recommendation of C was
wrong: the link stage has the same fail-fast shape as the children stage, so there is no
stage where a bounded fan-out is free. Every option that parallelises anything inside
this workflow (B, C) trades the determinism of "which SKU failed" for latency on a path
the audit itself measured as fitting its budget ("fine, but linear and fragile"). D is a
redesign of the write path.

So the honest choice is between **A** — keep the contract, accept the latency — and
**B** — pay the contract change to get the latency. There is no free option, and the
earlier note claimed one existed.

## If you choose C

The work is: fan out the link stage under the existing bound, keep the first-error
report, and a test asserting 50 links with at most 8 in flight and one failing link
naming its child.
