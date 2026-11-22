# Translations

The documentation is written in English and translated here. English is canonical: when a
translation and the English page disagree, the English page is right.

## What is translated, and what is not

Translated — the pages a reader needs to get started and to use the gateway:

| Page | 中文 | Español | Nederlands |
|---|---|---|---|
| Tutorial | [中文](zh/tutorial.md) | [Español](es/tutorial.md) | [Nederlands](nl/tutorial.md) |
| Getting started (README excerpt) | [中文](zh/README.md) | [Español](es/README.md) | [Nederlands](nl/README.md) |

**Not translated, on purpose:** the ADRs, the conformance matrix, the audit reports and the
operations runbooks. They are read by people who maintain the project rather than by people
adopting it, they change often, and a stale translation of a design decision is worse than no
translation — someone will act on it.

If you need one of those in another language, open an issue rather than translating it
silently.

## Keeping a translation honest

Each translated page starts with the English commit it was translated from:

```text
<!-- translated-from: docs/tutorial.md @ 0326e3c -->
```

That line is the contract. When the English page changes materially, the translation is
either updated or the line is left pointing at the older commit so a reader can see it is
behind. It is not silently bumped.

## Contributing a translation

1. Translate the current English page in full; do not summarise.
2. Keep code blocks, tool names, config keys and error codes **exactly** as they are. A
   translated `confirmation_required` is a bug.
3. Update the `translated-from` line to the commit you worked from.
4. Run `npx markdownlint-cli2 "**/*.md"` and `npm run lint:links`.

A new language needs an entry in the table above and a directory beside the existing ones.
