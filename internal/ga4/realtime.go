// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// realtimeDimensionAllowlist is the closed dimension set realtime reporting
// exposes. The realtime schema is stricter than the standard report schema.
var realtimeDimensionAllowlist = []string{"eventName", "country", "deviceCategory", "unifiedScreenName"}

// realtimeMetricAllowlist is the closed metric set realtime reporting exposes.
var realtimeMetricAllowlist = []string{"activeUsers", "eventCount", "screenPageViews"}

const (
	defaultRealtimeLimit = 100
	maxRealtimeLimit     = 100
)

// RealtimeQuery is one properties.runRealtimeReport request over the fixed
// 30-minute realtime window. Dimensions may be empty; at least one metric is
// required and Limit is capped at 100 rows.
type RealtimeQuery struct {
	Dimensions []string
	Metrics    []string
	Limit      int64
}

// Realtime runs a realtime report over the last 30 minutes and returns rows
// plus the property metadata. Realtime rows carry no date dimension, so the
// reused TrafficRow.Date field holds the row's dimension values joined by
// " | " (empty when the query has no dimensions). Every failure uses the same
// error taxonomy as the standard client.
func (s *Service) Realtime(ctx context.Context, query RealtimeQuery) (*ReportResult, error) {
	const op = "ga4.realtime"
	dimensions, metrics, limit, invalid := realtimeQuery(query)
	if invalid != nil {
		return nil, invalid.WithOp(op)
	}
	body, err := s.client.do(ctx, op, http.MethodPost, s.client.propertyPath(":runRealtimeReport"), ReportRequest{
		Dimensions: dimensions,
		Metrics:    metrics,
		Limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	var response ReportResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "GA4 returned an unreadable realtime response").WithOp(op).WithCause(err)
	}
	return newRealtimeResult(&response, metrics), nil
}

// realtimeQuery validates the allowlists and the realtime compatibility rules,
// then normalizes the row limit.
func realtimeQuery(query RealtimeQuery) ([]string, []string, int64, *errs.Error) {
	dimensions, invalid := allowlistedRealtime("dimension", query.Dimensions, realtimeDimensionAllowlist)
	if invalid != nil {
		return nil, nil, 0, invalid
	}
	metrics, invalid := allowlistedRealtime("metric", query.Metrics, realtimeMetricAllowlist)
	if invalid != nil {
		return nil, nil, 0, invalid
	}
	if len(metrics) == 0 {
		return nil, nil, 0, errs.New(errs.CodeValidationFailed, "at least one realtime metric is required").WithDetail("reason", "missing_metric")
	}
	if err := ValidateRealtime(dimensions, metrics); err != nil {
		var typed *errs.Error
		if errors.As(err, &typed) {
			return nil, nil, 0, typed
		}
		return nil, nil, 0, errs.New(errs.CodeValidationFailed, "invalid realtime dimension and metric combination").WithCause(err)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = defaultRealtimeLimit
	}
	if limit > maxRealtimeLimit {
		limit = maxRealtimeLimit
	}
	return dimensions, metrics, limit, nil
}

func allowlistedRealtime(kind string, names, allowed []string) ([]string, *errs.Error) {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !slices.Contains(allowed, name) {
			shown := name
			if len(shown) > 64 {
				shown = shown[:64]
			}
			return nil, errs.New(errs.CodeValidationFailed,
				fmt.Sprintf("%s %q is not supported by realtime reporting; allowed: %s", kind, shown, strings.Join(allowed, ", "))).
				WithDetail("reason", "unsupported_"+kind).WithDetail(kind, shown)
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

func newRealtimeResult(response *ReportResponse, metrics []string) *ReportResult {
	result := &ReportResult{
		Rows:     make([]TrafficRow, 0, len(response.Rows)),
		Totals:   map[string]float64{},
		TimeZone: response.Metadata.TimeZone,
		Currency: response.Metadata.CurrencyCode,
		Sampled:  response.Metadata.Sampled,
	}
	for _, row := range response.Rows {
		values := make(map[string]float64, len(metrics))
		for i, name := range metrics {
			if i < len(row.Metrics) {
				values[name] = row.Metrics[i]
			}
		}
		result.Rows = append(result.Rows, TrafficRow{Date: strings.Join(row.Dimensions, " | "), Metrics: values})
	}
	return result
}
