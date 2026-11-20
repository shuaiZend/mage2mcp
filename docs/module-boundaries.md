# Module boundaries and public contracts

**Status:** design reference. The ownership table and the error taxonomy in §4.2
are as-built; the other interface snippets describe the intended shape where the
implementation differs (for example `internal/core/datasource` is declared but no
module implements it). Changing a public contract requires an ADR and Architect
review.

This document defines what each module owns, what it may import, and the Go
contracts that cross module boundaries. The error enum in §4.2 is the shipped
closed set in `internal/core/errors/errors.go`; the other snippets describe the
intended shape where the implementation differs.

## 1. Principles

1. **A module owns its data source end to end** — client, service, DTOs,
   configuration, errors, tools, tests, and documentation live together.
2. **No module imports another module.** Cross-source work is expressed as
   narrow interfaces declared by the consumer (`internal/unified`) over neutral
   types in `internal/core/models`, and wired in `cmd/server`.
3. **Interfaces are small.** A module exposes only what a tool or a consumer
   actually needs. Internal helpers stay unexported.
4. **Neutral types live in `core`.** Money, time ranges, pagination, SKU,
   instance references, and error codes are defined once, in `core`.
5. **Tools are thin.** A tool validates, authorizes, and delegates. Business
   rules live in services, never in tool handlers.

## 2. Ownership

| Path | Owns | Reviewer required |
|---|---|---|
| `internal/core/` | Contracts, error taxonomy, neutral models, config schema | Architect |
| `internal/security/` | AuthN, RBAC, audit, secrets | Architect + Security |
| `internal/mcp/` | Server, registry, transport, tool definitions | Architect |
| `internal/magento/` | Magento client, product/EAV/configurable/inventory/import/order services | Magento owner |
| `internal/cloudflare/` | Cloudflare client, GraphQL templates, security events | Cloudflare owner |
| `internal/ga4/` | GA4 client, report builder, compatibility validator | GA4 owner |
| `internal/unified/` | Cross-source comparison (`traffic.compare`); other scenarios are PLANNED | Architect + Unified owner |
| `internal/jobs/` | Job definitions, queues, retries | Architect |
| `internal/storage/` | Repositories and the embedded migration runner | Architect |
| `internal/observability/` | Logging and metrics (health endpoints live in `internal/mcp/server`) | Architect |
| `cmd/` | Wiring and process lifecycle only | Architect |
| `test/`, `internal/*/*_test.go` | Cross-cutting e2e harness and module tests | QA |
| `docs/` | Design and operations documentation | Docs owner |

`cmd/` contains wiring and nothing else. A `main` package with business logic
is a review failure.

## 3. Dependency rules

Allowed imports:

| From | May import |
|---|---|
| `internal/core` | standard library, `gopkg.in/yaml.v3`, validation helpers |
| `internal/security` | `core` |
| `internal/admin` | `core`, `security` (approval surface writes confirmation decisions) |
| `internal/logs` | `core` (request-log queries and ingestion) |
| `internal/mcp` | `core`, `security`, module public packages (for registration) |
| `internal/magento` | `core` |
| `internal/cloudflare` | `core` |
| `internal/ga4` | `core` |
| `internal/unified` | `core` only |
| `internal/jobs` | `core`, module services (for task handlers) |
| `internal/storage` | `core` |
| `internal/observability` | `core` |

Forbidden, without exception:

- `internal/magento` importing `internal/cloudflare`, `internal/ga4`, or `internal/unified`.
- `internal/cloudflare` importing `internal/magento` or `internal/ga4`.
- `internal/ga4` importing `internal/magento` or `internal/cloudflare`.
- Any module importing `internal/mcp` (tools depend on services, never the reverse).
- Any module importing `cmd/`.
- `internal/unified` importing a module package.
- Cyclic imports of any kind.

Phase 1 adds a check (a small analyzer or a `go list -deps` script) that fails
CI when a forbidden edge appears. Until then, review enforces the table above.

## 4. Shared contracts (`internal/core`)

### 4.1 Data source

```go
package datasource

// Capability is a declared, versioned unit of functionality. The registry
// registers tools only for capabilities that are present and enabled.
type Capability struct {
    ID          string // e.g. "magento.product.write"
    Version     string // semver of the capability contract
    ReadOnly    bool
    Description string
}

// DataSource is implemented by every integration module. It is intentionally
// small: modules must not be forced to implement methods that do not apply.
type DataSource interface {
    Name() string
    HealthCheck(ctx context.Context) error
    // Capabilities is instance-scoped: a Magento Open Source instance lacks
    // import, a Pro Cloudflare zone lacks bot fields, a GA4 property may lack
    // ecommerce. The registry registers a tool when at least one enabled
    // instance supports it and returns unsupported_capability otherwise.
    Capabilities(ctx context.Context, instanceID string) ([]Capability, error)
}
```

### 4.2 Errors

```go
package errs

type Code string

const (
    CodeInvalidArgument      Code = "invalid_argument"
    CodeValidationFailed     Code = "validation_failed"
    CodeUnauthenticated      Code = "unauthenticated"
    CodePermissionDenied     Code = "permission_denied"
    CodeNotFound             Code = "not_found"
    CodeConflict             Code = "conflict"
    CodeRateLimited          Code = "rate_limited"
    CodeUpstreamTimeout      Code = "upstream_timeout"
    CodeUpstreamUnavailable  Code = "upstream_unavailable"
    CodeUnsupportedCapability Code = "unsupported_capability"
    CodeConfirmationRequired Code = "confirmation_required"
    CodeInternal             Code = "internal"

    // Domain-specific codes required by dependent documents. They are part of the
    // closed set and covered by contract tests; modules may not invent new codes.
    CodeIllegalTransition        Code = "illegal_transition"
    CodeIdempotencyKeyReuse      Code = "idempotency_key_reuse"
    // CodeOperationUnknown means the mutation may or may not have been applied
    // upstream. It is never a licence to retry: poll the operation until the
    // reconciler resolves it. Returned by the money/order tools and by any gated
    // write whose outcome is ambiguous.
    CodeOperationUnknown        Code = "operation_unknown"
    CodeArtifactExpired          Code = "artifact_expired"
    CodeConfirmationMismatch     Code = "confirmation_param_mismatch"
    CodeConfirmationUsed         Code = "confirmation_used"
    CodeConfirmationExpired      Code = "confirmation_expired"
    CodeEcommerceNotTracked      Code = "ecommerce_not_tracked"
    CodeRequiresLogpush          Code = "requires_logpush"
    CodeInsufficientData         Code = "insufficient_data"
    CodeMappingIncomplete        Code = "mapping_incomplete"
    CodeCurrencyMismatch         Code = "currency_mismatch"
    CodeIncompleteWindow         Code = "incomplete_window"
    CodeUnsupportedFormat        Code = "unsupported_format"
    CodeCredentialScopeExceeded  Code = "credential_scope_exceeded"
    CodeCredentialScopeInsufficient Code = "credential_scope_insufficient"
    CodeProbeSKUConflict         Code = "probe_sku_conflict"
)

// This enum is the shipped closed set; `errors_test.go` and the toolerr tests
// pin it. Adding a code is a taxonomy change and must update this document and
// the conformance tests.

type Error struct {
    Code      Code
    Message   string            // safe to show to an agent
    Retryable bool
    Upstream  string            // "magento", "cloudflare", "ga4", ""
    Op        string            // "product.get", "report.run", ...
    Details   map[string]string // bounded, redacted
    cause     error
}
```

Rules:

- `Message` never contains tokens, URLs with credentials, stack traces, or raw
  upstream bodies.
- `Details` is limited to 10 keys and 1 KiB total, and is redacted through the
  shared redactor before being attached.
- `cause` is preserved for logs only and never serialized to the agent.
- Mapping from upstream errors to `Code` is each module's responsibility and is
  covered by table-driven tests.

### 4.3 Job enqueue

```go
package jobs

// Enqueuer is the only async dependency a tool handler may take. The concrete
// implementation lives in internal/jobs and is injected in cmd/server, so no
// module imports the jobs package and the dependency table stays acyclic.
type Scope struct {
    InstanceID string // exactly one of InstanceID or BindingID is set
    BindingID  string
}

type Task struct {
    Kind           string          // registered handler name
    Scope          Scope
    Payload        json.RawMessage // typed request, never a secret
    IdempotencyKey string
    MaxAttempts    int             // 1 for writes and imports
}

type JobState struct {
    ID           string
    Kind         string
    State        string // queued, running, canceling, succeeded, failed, canceled
    AttemptCount int
    ErrorCode    string
}

type Enqueuer interface {
    Enqueue(ctx context.Context, task Task) (jobID string, err error)
    Status(ctx context.Context, jobID string) (*JobState, error)
    Cancel(ctx context.Context, jobID string) error
}
```

Modules that start long-running work accept an `Enqueuer` in their constructor.
Import, report, and ingestion jobs are registered by the worker, which is the only
place that knows the handler map.

### 4.4 Pagination and time

```go
package models

type Page[T any] struct {
    Items      []T
    NextCursor string
    TotalKnown *int64
}

type TimeRange struct {
    Start time.Time // UTC
    End   time.Time // UTC
    Zone  string    // IANA zone name, e.g. "America/Los_Angeles"; resolved internally
}

type Money struct {
    Amount   *string // decimal string; nil means unknown or mixed-currency
    Currency string  // ISO-4217; empty when Mixed is true
    Mixed    bool    // true when sources disagree on currency and no rate was supplied
}
```

Rules:

- Cursors are opaque, signed, and instance-bound. They are not offsets.
- All timestamps cross the boundary in UTC; presentation in a local zone is a
  property of the result, not of the storage.
- Money is never a float. Currency conversion is never implicit.

## 5. Module service interfaces

These are the contracts tools and `unified` depend on. Every method takes
`instanceID` because one deployment may serve several Magento stores, Cloudflare
zones, or GA4 properties.

### 5.1 Magento (`internal/magento`)

```go
type ProductService interface {
    Get(ctx context.Context, instanceID, sku string) (*models.Product, error)
    Search(ctx context.Context, instanceID string, q ProductQuery) (models.Page[models.Product], error)
    Create(ctx context.Context, instanceID string, in CreateProductInput) (*models.Product, error)
    Update(ctx context.Context, instanceID, sku string, in UpdateProductInput) (*models.Product, error)
    Delete(ctx context.Context, instanceID, sku string) error
}

type ConfigurableService interface {
    Get(ctx context.Context, instanceID, sku string) (*models.ConfigurableProduct, error)
    Create(ctx context.Context, instanceID string, in CreateConfigurableInput) (*models.ConfigurableProduct, error)
    Update(ctx context.Context, instanceID, sku string, in UpdateConfigurableInput) (*models.ConfigurableProduct, error)
    AddChild(ctx context.Context, instanceID, parentSKU, childSKU string) error
    RemoveChild(ctx context.Context, instanceID, parentSKU, childSKU string) error
    Children(ctx context.Context, instanceID, parentSKU string) ([]models.Product, error)
    SetOptions(ctx context.Context, instanceID, parentSKU string, options []VariantOption) (*models.ConfigurableProduct, error)
    CreateVariations(ctx context.Context, instanceID string, in CreateVariationsInput) (*VariationResult, error)
    VerifyStorefront(ctx context.Context, instanceID, sku string) (*StorefrontVerification, error)
}

type AttributeService interface {
    List(ctx context.Context, instanceID string, q AttributeQuery) ([]models.Attribute, error)
    Get(ctx context.Context, instanceID, code string) (*models.Attribute, error)
    Options(ctx context.Context, instanceID, code string) ([]models.AttributeOption, error)
    Sets(ctx context.Context, instanceID string) ([]models.AttributeSet, error)
    SetAttributes(ctx context.Context, instanceID string, setID int) ([]models.Attribute, error)
}

type InventoryService interface {
    Sources(ctx context.Context, instanceID string) ([]models.InventorySource, error)
    Stocks(ctx context.Context, instanceID string) ([]models.InventoryStock, error)
    SourceItems(ctx context.Context, instanceID string, q SourceItemQuery) (models.Page[models.SourceItem], error)
    AssignSourceItems(ctx context.Context, instanceID string, items []SourceItemInput) (*BulkResult, error)
    UnassignSourceItems(ctx context.Context, instanceID string, items []SourceItemRef) (*BulkResult, error)
    Transfer(ctx context.Context, instanceID string, in TransferInput) (*BulkResult, error)
}

type CategoryService interface {
    List(ctx context.Context, instanceID string) ([]models.Category, error)
    Get(ctx context.Context, instanceID string, categoryID int) (*models.Category, error)
    Create(ctx context.Context, instanceID string, in CreateCategoryInput) (*models.Category, error)
    Update(ctx context.Context, instanceID string, categoryID int, in UpdateCategoryInput) (*models.Category, error)
    AssignProducts(ctx context.Context, instanceID string, categoryID int, skus []string) error
}

type MediaService interface {
    List(ctx context.Context, instanceID, sku string) ([]models.MediaEntry, error)
    Upload(ctx context.Context, instanceID, sku string, in MediaUploadInput) (*models.MediaEntry, error)
    Delete(ctx context.Context, instanceID, sku string, entryID int) error
}

type OrderService interface {
    Get(ctx context.Context, instanceID, orderID string) (*models.Order, error)
    Search(ctx context.Context, instanceID string, q OrderQuery) (models.Page[models.Order], error)
    Summary(ctx context.Context, instanceID string, r models.TimeRange) (*SalesSummary, error)
}

type ImportService interface {
    Validate(ctx context.Context, instanceID string, in ImportRequest) (*ImportPreview, error)
    Start(ctx context.Context, instanceID string, in ImportRequest) (*ImportBatch, error)
    Status(ctx context.Context, instanceID, batchID string) (*ImportBatch, error)
    Resume(ctx context.Context, instanceID, batchID string) (*ImportBatch, error)
    Cancel(ctx context.Context, instanceID, batchID string) error
    Report(ctx context.Context, instanceID, batchID string, cursor string) (models.Page[ImportItemResult], error)
}
```

### 5.2 Cloudflare (`internal/cloudflare`)

```go
type AnalyticsService interface {
    Query(ctx context.Context, instanceID string, q AnalyticsQuery) (*AnalyticsResult, error)
    Datasets(ctx context.Context, instanceID string) ([]Dataset, error)
    Budget(ctx context.Context, instanceID string) (*BudgetStatus, error)
}

type SecurityService interface {
    Events(ctx context.Context, instanceID string, q SecurityEventQuery) (models.Page[SecurityEvent], error)
    Summary(ctx context.Context, instanceID string, q SecuritySummaryQuery) (*SecuritySummary, error)
}

type ZoneService interface {
    List(ctx context.Context, instanceID string) ([]Zone, error)
}

type RequestLogService interface {
    // Request-level logs come from the optional R2/ClickHouse pipeline, never
    // from the GraphQL analytics datasets. Returns unsupported_capability when
    // Logpush ingestion is not configured.
    Query(ctx context.Context, instanceID string, q models.RequestLogQuery) (models.Page[models.RequestLogEntry], error)
}

type LogsService interface {
    // Read-only by construction: no create, update, or delete methods exist.
    ListJobs(ctx context.Context, instanceID string) ([]LogpushJob, error)
    GetJob(ctx context.Context, instanceID string, jobID int64) (*LogpushJob, error)
    ListDatasets(ctx context.Context, instanceID string) ([]LogpushDataset, error)
}
```

`AnalyticsQuery` is a typed query over an allowlisted set of templates, never a
raw GraphQL string.

### 5.3 GA4 (`internal/ga4`)

```go
type AnalyticsService interface {
    Report(ctx context.Context, instanceID string, q ReportQuery) (*ReportResult, error)
    BatchReport(ctx context.Context, instanceID string, qs []ReportQuery) ([]*ReportResult, error)
    PivotReport(ctx context.Context, instanceID string, q PivotQuery) (*ReportResult, error)
    Realtime(ctx context.Context, instanceID string, q RealtimeQuery) (*ReportResult, error)
    Metadata(ctx context.Context, instanceID string) (*PropertyMetadata, error)
    Capabilities(ctx context.Context, instanceID string) (*PropertyCapabilities, error)
}
```

`ReportResult` always carries `TimeZone`, `Currency`, `Freshness`, and
`Quality` metadata. A result without those fields is a contract violation.

### 5.4 Unified (`internal/unified`)

`unified` declares the narrow interfaces it consumes. Every signature uses neutral
types from `internal/core/models`; module DTOs never appear here. Adapters in
`cmd/server` translate module results into those neutral types.

```go
type OrderSource interface {
    SalesSummary(ctx context.Context, instanceID string, r models.TimeRange) (*models.SalesSummary, error)
}

type TrafficSource interface {
    TrafficSummary(ctx context.Context, instanceID string, q models.TrafficQuery) (*models.TrafficSummary, error)
}

type SessionSource interface {
    SessionSummary(ctx context.Context, instanceID string, q models.SessionQuery) (*models.SessionSummary, error)
}

type Binding struct {
    ID        string
    Magento   string
    Cloudflare string
    GA4       string
}

type TrafficService interface {
    Compare(ctx context.Context, b Binding, q TrafficComparison) (*TrafficComparisonResult, error)
    Geography(ctx context.Context, b Binding, q GeographyQuery) (*GeographyResult, error)
}

type EcommerceService interface {
    ProductFunnel(ctx context.Context, b Binding, q ProductFunnelQuery) (*ProductFunnelResult, error)
    ChannelRevenue(ctx context.Context, b Binding, q ChannelRevenueQuery) (*ChannelRevenueResult, error)
}

type SecurityService interface {
    BotVsHuman(ctx context.Context, b Binding, q BotTrafficQuery) (*BotTrafficResult, error)
    WAFImpact(ctx context.Context, b Binding, q WAFImpactQuery) (*WAFImpactResult, error)
}

type AnalyticsService interface {
    SKUPerformance(ctx context.Context, b Binding, q SKUPerformanceQuery) (*SKUPerformanceResult, error)
    DeclineCorrelation(ctx context.Context, b Binding, q DeclineQuery) (*DeclineResult, error)
}

type MappingService interface {
    Propose(ctx context.Context, b Binding, q MappingProposalQuery) (*MappingProposal, error)
    Confirm(ctx context.Context, b Binding, proposalID string) (*Mapping, error)
}
```

The neutral types named above (`SalesSummary`, `TrafficQuery`, `TrafficSummary`,
`SessionQuery`, `SessionSummary`, `Binding`, and every unified query/result type)
are part of `core/models`. Adding one requires an ADR only if it changes an existing
field; adding a new type is additive.

## 6. DTO rules

- DTOs are explicit structs. `map[string]any` is forbidden in public signatures.
- Every DTO field that can be absent is a pointer or has an explicit presence
  flag; zero values are never overloaded to mean "unset".
- JSON tags are lower_snake_case for tool I/O; internal DTOs do not leak
  upstream field names.
- Upstream identifiers (SKU, order ID, property ID, zone ID) are strings at the
  boundary, even when Magento uses integers internally.
- Enums are typed strings with a `Valid()` method; unknown upstream values map
  to `"unknown"` and are preserved in `Raw` only when a debug mode is enabled.
- DTOs never contain credentials, full upstream payloads, or PII beyond what the
  tool contract declares.

## 7. Tool contract

Every tool is declared with:

| Field | Rule |
|---|---|
| `Name` | `module.domain.action`, lower_snake after the second dot |
| `Title` | Human-readable, ≤ 60 chars |
| `Description` | States what it does, what it does not do, and the data source |
| `InputSchema` | JSON Schema 2020-12, `additionalProperties: false`, all required fields listed |
| `OutputSchema` | Present for structured results; omitted only for free-form text |
| `Capability` | Capability ID that must be present |
| `Permission` | Permission string checked by RBAC |
| `Timeout` | Explicit; default 30 s, max 120 s for synchronous tools |
| `Annotations` | `readOnlyHint` accurate; `destructiveHint`/`idempotentHint` set for writes |
| `Audit` | Every call records identity, instance, tool, param hash, outcome |

A tool handler must not:

- build upstream URLs from user input,
- call an upstream API directly (it goes through a service),
- catch a typed error and return a raw string,
- perform more than one logical write without an idempotency key.

## 8. Contract tests and fakes

Each module ships:

- a `fake` implementation of its public interfaces in `internal/<module>/faketest`
  (test-only package) so `unified`, `mcp`, and `jobs` can be tested without the
  real upstream;
- table-driven contract tests that both the fake and the real service must pass;
- recorded-response fixtures with secrets and PII scrubbed, stored under
  `tests/fixtures/<module>/`.

`unified` tests use fakes only. No test in `internal/unified` may require a
network connection.

## 9. Interface change process

1. Open an issue describing the change and why the current contract is
   insufficient.
2. Write an ADR if the change is architectural, or amend the owning document if
   it is additive.
3. Update this document and the contract tests in the same PR.
4. Get Architect review; security-relevant changes also need Security review.
5. Deprecate before removing: mark the method, keep it for one minor release,
   log a deprecation warning, then remove in the next minor.

## 10. Enforcement

| Mechanism | Status |
|---|---|
| CODEOWNERS review routing | Active |
| Import-boundary check in CI | Not implemented (review-only) |
| `go vet` + `golangci-lint` | Active in `scripts/local-ci.sh` and manual CI |
| Contract tests with fakes | Implemented |
| Public API diff check (breaking change detection) | PLANNED |
| ADR required for interface changes | Active process |

## 11. Anti-patterns

- A module exposing its HTTP client so another module can "just make one call".
- A shared `utils` package that accumulates domain logic.
- A tool handler that re-implements validation already enforced by the service.
- A `unified` function that reaches into a module's DTO to fix a field name.
- An interface with more than seven methods "for completeness".
- Passing `context.Background()` inside a service.
- Logging a full upstream response "for debugging" at info level.

## 12. Open questions

| Question | Owner | Resolution point |
|---|---|---|
| Whether `jobs` may import module packages directly or must use task interfaces | Architect | ADR before Phase 3 |
| Whether `unified` needs a caching layer of its own | Architect | Phase 5 |
| Public Go SDK for third-party integrations | Maintainer | Post-1.0 |
