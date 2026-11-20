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
| **C** | Parallelise only the **link** stage (children stay sequential) | One stage, no contract change: links are independent and a failed link already names its child | Low. Saves ~50 round-trips at the 50-child maximum |
| **D** | Use Magento's async bulk endpoints where available | Feature detection per store, a different result model (poll instead of respond), and the ledger's "one call, one operation" premise no longer holds | High. This is a redesign of the write path, not an optimisation |

## Recommendation

**C now, D never without a separate design.** The link stage is where a bounded fan-out
costs nothing: links are independent, the failure already names its child, and the
compensation story does not change. Children creation keeps its fail-fast contract,
because the alternative (B) makes the failure report less useful in exchange for
latency on a path that already fits its budget.

## If you choose C

The work is: fan out the link stage under the existing bound, keep the first-error
report, and a test asserting 50 links with at most 8 in flight and one failing link
naming its child.
