# Magento 2 module

Read and write access to a Magento 2.4.x store through its official REST API.
No plugin is installed and the Magento database is never touched.

## Public surface

- `NewClient(ClientConfig)` — bounded REST client (`/rest/{store}/V1`), OAuth
  1.0a or bearer signing, 8 MiB response cap, error taxonomy mapping.
- `NewService(*Client)` — product reads and writes, configurable lifecycle, MSI
  source items, bounded import validation and execution, order reads and a
  bounded per-day sales summary, category reads/tree, and attribute-set reads.
- `Register(reg, svc)` — read tools; `RegisterWrites(reg, svc)` — write tools
  (require confirmation and `tools.writes`).

## Behavior notes

- Configurable creation is children → parent options → links. Failures report
  the stage, completed SKUs, and failed SKU; writes are **not** rolled back.
- Import execution is bounded (8 MiB, 5000 rows) and non-atomic; new products
  require `attribute_set_id`.
- Client IPs, credentials, and raw upstream bodies never appear in errors.

## Tests

`go test -race ./internal/magento/...` — fake REST upstream, OAuth signature
vector, error mapping, tool registration, in-memory MCP calls.
