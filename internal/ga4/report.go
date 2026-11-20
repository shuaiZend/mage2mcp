// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// DateRange is an inclusive reporting window. Dates are YYYY-MM-DD or a GA4
// relative form such as 7daysAgo or yesterday.
type DateRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

// OrderBy sorts report rows by exactly one dimension or metric.
type OrderBy struct {
	Dimension string
	Metric    string
	Desc      bool
}

// ReportRequest is one properties.runReport request.
type ReportRequest struct {
	Dimensions         []string
	Metrics            []string
	DateRanges         []DateRange
	Limit              int64
	Offset             int64
	OrderBys           []OrderBy
	DimensionFilter    map[string]any
	MetricAggregations []string
}

// Row is one report row: dimension values and metric values in header order.
type Row struct {
	Dimensions []string
	Metrics    []float64
}

// Metadata is the property-level context of a report response.
type Metadata struct {
	CurrencyCode         string `json:"currencyCode"`
	TimeZone             string `json:"timeZone"`
	Sampled              bool   `json:"-"`
	DataLossFromOtherRow bool   `json:"dataLossFromOtherRow"`
}

// UnmarshalJSON decodes the response metadata and derives Sampled from GA4's
// sampling metadata list.
func (m *Metadata) UnmarshalJSON(data []byte) error {
	var wire struct {
		CurrencyCode         string            `json:"currencyCode"`
		TimeZone             string            `json:"timeZone"`
		SamplingMetadatas    []json.RawMessage `json:"samplingMetadatas"`
		DataLossFromOtherRow bool              `json:"dataLossFromOtherRow"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*m = Metadata{
		CurrencyCode:         wire.CurrencyCode,
		TimeZone:             wire.TimeZone,
		Sampled:              len(wire.SamplingMetadatas) > 0,
		DataLossFromOtherRow: wire.DataLossFromOtherRow,
	}
	return nil
}

// ReportResponse is a parsed properties.runReport response. Row and Metadata
// decode themselves from the Data API JSON.
type ReportResponse struct {
	Rows     []Row    `json:"rows" jsonschema:"Per-row results, in input order."`
	Totals   []Row    `json:"totals" jsonschema:"Totals across every row, computed by the store."`
	RowCount int64    `json:"rowCount"`
	Metadata Metadata `json:"metadata"`
}

// UnmarshalJSON decodes one Data API row: dimension values and numeric metric
// values in header order.
func (row *Row) UnmarshalJSON(data []byte) error {
	var wire struct {
		DimensionValues []struct {
			Value string `json:"value" jsonschema:"Value of the entry."`
		} `json:"dimensionValues"`
		MetricValues []struct {
			Value string `json:"value" jsonschema:"Value of the entry."`
		} `json:"metricValues"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	row.Dimensions = make([]string, len(wire.DimensionValues))
	for i, value := range wire.DimensionValues {
		row.Dimensions[i] = value.Value
	}
	row.Metrics = make([]float64, len(wire.MetricValues))
	for i, value := range wire.MetricValues {
		raw := strings.TrimSpace(value.Value)
		if raw == "" {
			continue
		}
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("ga4: non-numeric metric value: %w", err)
		}
		row.Metrics[i] = parsed
	}
	return nil
}

// trafficMetricAllowlist is the closed metric set the traffic tool exposes.
var trafficMetricAllowlist = []string{
	"sessions", "totalUsers", "newUsers", "screenPageViews", "engagementRate",
	"averageSessionDuration", "bounceRate", "eventCount", "keyEvents",
}

// TrafficQuery is a traffic report request.
type TrafficQuery struct {
	StartDate string
	EndDate   string
	Metrics   []string
}

// TrafficRow is one date row of a traffic report.
type TrafficRow struct {
	Date    string             `json:"date" jsonschema:"Calendar day, YYYY-MM-DD."`
	Metrics map[string]float64 `json:"metrics" jsonschema:"Metrics the report returned."`
}

// ReportResult is the structured result of a traffic report. Totals are the
// period aggregates computed by GA4; they are never the sum of Rows.
type ReportResult struct {
	Rows     []TrafficRow       `json:"rows" jsonschema:"Per-row results, in input order."`
	Totals   map[string]float64 `json:"totals" jsonschema:"Totals across every row, computed by the store."`
	TimeZone string             `json:"time_zone" jsonschema:"IANA time zone the report was computed in."`
	Currency string             `json:"currency" jsonschema:"ISO 4217 code the amounts are expressed in."`
	Sampled  bool               `json:"sampled" jsonschema:"True when the answer is a sample rather than the full set."`
}

// PropertyMetadata lists the dimension and metric API names of a property.
type PropertyMetadata struct {
	Dimensions []string `json:"dimensions" jsonschema:"Dimensions the report was grouped by."`
	Metrics    []string `json:"metrics" jsonschema:"Metrics the report returned."`
}

// Service is the GA4 reporting service for one configured property.
type Service struct {
	client *Client
}

// NewService validates cfg and returns a reporting service.
func NewService(cfg Config) (*Service, error) {
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Service{client: client}, nil
}

const (
	defaultTrafficStart = "7daysAgo"
	defaultTrafficEnd   = "yesterday"
	trafficRowLimit     = 1000
)

// Traffic runs a date-grouped traffic report and returns rows plus period
// totals. An empty date range defaults to the last seven days.
func (s *Service) Traffic(ctx context.Context, query TrafficQuery) (*ReportResult, error) {
	const op = "ga4.traffic"
	metrics, invalid := trafficMetrics(query.Metrics)
	if invalid != nil {
		return nil, invalid.WithOp(op)
	}
	start, end := query.StartDate, query.EndDate
	switch {
	case start == "" && end == "":
		start, end = defaultTrafficStart, defaultTrafficEnd
	case start == "" || end == "":
		return nil, errs.New(errs.CodeValidationFailed, "start_date and end_date must be provided together").
			WithDetail("reason", "invalid_range").WithOp(op)
	}
	response, err := s.client.runReport(ctx, ReportRequest{
		Dimensions:         []string{"date"},
		Metrics:            metrics,
		DateRanges:         []DateRange{{StartDate: start, EndDate: end}},
		Limit:              trafficRowLimit,
		OrderBys:           []OrderBy{{Dimension: "date"}},
		MetricAggregations: []string{"TOTAL"},
	})
	if err != nil {
		return nil, err
	}
	return newReportResult(response, metrics), nil
}

// Metadata returns the dimension and metric API names available on the property.
func (s *Service) Metadata(ctx context.Context) (*PropertyMetadata, error) {
	const op = "ga4.metadata"
	body, err := s.client.do(ctx, op, http.MethodGet, s.client.propertyPath("/metadata"), nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Dimensions []metadataField `json:"dimensions" jsonschema:"Dimensions the report was grouped by."`
		Metrics    []metadataField `json:"metrics" jsonschema:"Metrics the report returned."`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "GA4 returned unreadable property metadata").WithOp(op).WithCause(err)
	}
	names := func(fields []metadataField) []string {
		out := make([]string, 0, len(fields))
		for _, field := range fields {
			if field.APIName != "" {
				out = append(out, field.APIName)
			}
		}
		sort.Strings(out)
		return out
	}
	return &PropertyMetadata{Dimensions: names(response.Dimensions), Metrics: names(response.Metrics)}, nil
}

type metadataField struct {
	APIName string `json:"apiName"`
}

func trafficMetrics(names []string) ([]string, *errs.Error) {
	if len(names) == 0 {
		return nil, errs.New(errs.CodeValidationFailed, "at least one traffic metric is required").WithDetail("reason", "missing_metric")
	}
	metrics := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !slices.Contains(trafficMetricAllowlist, name) {
			shown := name
			if len(shown) > 64 {
				shown = shown[:64]
			}
			return nil, errs.New(errs.CodeValidationFailed,
				fmt.Sprintf("metric %q is not a supported traffic metric; allowed: %s", shown, strings.Join(trafficMetricAllowlist, ", "))).
				WithDetail("reason", "unsupported_metric").WithDetail("metric", shown)
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		metrics = append(metrics, name)
	}
	return metrics, nil
}

func newReportResult(response *ReportResponse, metrics []string) *ReportResult {
	values := func(row Row) map[string]float64 {
		out := make(map[string]float64, len(metrics))
		for i, name := range metrics {
			if i < len(row.Metrics) {
				out[name] = row.Metrics[i]
			}
		}
		return out
	}
	result := &ReportResult{
		Rows:     make([]TrafficRow, 0, len(response.Rows)),
		TimeZone: response.Metadata.TimeZone,
		Currency: response.Metadata.CurrencyCode,
		Sampled:  response.Metadata.Sampled,
		Totals:   map[string]float64{},
	}
	for _, row := range response.Rows {
		traffic := TrafficRow{Metrics: values(row)}
		if len(row.Dimensions) > 0 {
			traffic.Date = row.Dimensions[0]
		}
		result.Rows = append(result.Rows, traffic)
	}
	if len(response.Totals) > 0 {
		result.Totals = values(response.Totals[0])
	}
	return result
}
