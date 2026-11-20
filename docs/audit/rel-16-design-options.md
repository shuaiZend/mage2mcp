# REL-16: the options, and what each costs

REL-16 is the last behavioural gap in the audit. This note exists so the decision is a
choice between concrete designs rather than an open question.

## The gap

For the 50 middleware-gated write tools, the outcome audit row is written in the same
transaction as the ledger transition, so it cannot go missing. For the five
self-ledgered money tools (`invoice.create`, `invoice.capture`, `creditmemo.create`,
`order.create`, `order.cancel`) the tool commits its own ledger row and the middleware
writes the audit row afterwards. A crash in that window leaves the `operations` row
correct but its audit row absent.

The ledger is still the authority on whether the money moved, so this is an audit-trail
gap, not a correctness gap.

## Why the obvious fix is wrong

The ledger already writes audit rows inside its own transactions for
`RecoverExpired` and `Abort`, so moving the outcome row there looks like a one-line
change. It is not: `CreateOrder` validates its input **before** `ops.Begin`, so a call
that fails validation opens no operation and would end up with **no audit row at all**.
That trades a narrow crash window for a systematic gap.

The real problem is that the middleware cannot tell whether the tool opened an
operation.

## Options

| # | Design | Cost | Risk |
|---|---|---|---|
| **A** | The tool signals it through the context: `ConfigureOperations` installs a per-call recorder, the tool marks it when it opens an operation, the middleware writes the audit row only when the mark is absent | ~40 lines across the five tools' shared helper and the middleware; one new context value | Low. No schema change, no extra query. The signal is explicit and greppable |
| **B** | The middleware asks the ledger after the call: recompute the args hash and look for a matching operation row, write the audit row only when none exists | One indexed query per write call | Low risk, but it adds latency to every write and couples the middleware to the ledger's key shape |
| **C** | The middleware writes an intent row before the handler and the ledger writes the outcome; a pre-`Begin` failure keeps the intent row | No new signal | Every self-ledgered call gains a row that says nothing happened, and the audit trail now needs readers to understand two row kinds |
| **D** | Accept the gap and record it | None | A crash between commit and audit write loses one row. The ledger still proves the mutation |

## Recommendation

**A.** It is the smallest change that closes the gap without weakening anything else,
and it makes the two write paths symmetric: whoever owns the operation owns its audit
row. B works but pays a query per write for information the tool already knows. C
changes what an audit trail means. D is defensible but leaves the last behavioural
finding open.

## If you choose A

The work is: a context-scoped marker set by the five tools' shared operation helper, a
read of that marker in the middleware's non-gated path, and a test that kills the
process between commit and audit write for one tool and asserts the row is present.
