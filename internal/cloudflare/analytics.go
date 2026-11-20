// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"fmt"
	"strconv"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	maxTrafficWindowDays = 31
	maxTrafficLimit      = 1000
)

// trafficQueryTemplate is the fixed document behind cloudflare.analytics.traffic.
// Only the dimensions block varies, rendered from the allowlist below.
const trafficQueryTemplate = `query ZoneTraffic($zoneTag: string, $since: Time, $until: Time, $limit: uint64) {
  viewer {
    zones(filter: { zoneTag: $zoneTag }) {
      httpRequests1hGroups(limit: $limit, filter: { datetime_geq: $since, datetime_lt: $until }, orderBy: [sum_requests_DESC]) {
        dimensions { %s }
        sum { requests bytes cachedRequests cachedBytes }
      }
    }
  }
}`

// TrafficQuery selects one dimension of hourly zone traffic.
type TrafficQuery struct {
	// Since is the inclusive start of the window.
	Since time.Time
	// Until is the exclusive end of the window.
	Until time.Time
	// GroupBy is one of "hostname", "country", or "status".
	GroupBy string
	// Limit caps the aggregate rows and must be within 1..1000.
	Limit int
}

// TrafficRow is one aggregate row for the whole window, not one hour.
type TrafficRow struct {
	Dimension      string  `json:"dimension"`
	Requests       float64 `json:"requests" jsonschema:"Requests counted."`
	Bytes          float64 `json:"bytes" jsonschema:"Bytes transferred."`
	CachedRequests float64 `json:"cached_requests" jsonschema:"Requests served from cache."`
	CachedBytes    float64 `json:"cached_bytes" jsonschema:"Bytes served from cache."`
}

// Traffic returns hourly traffic aggregates for one dimension of the zone.
func (s *Service) Traffic(ctx context.Context, q TrafficQuery) ([]TrafficRow, error) {
	const op = "cloudflare.analytics.traffic"
	client, err := s.clientOrError(op)
	if err != nil {
		return nil, err
	}
	alias, ok := trafficDimensionAlias(q.GroupBy)
	if !ok {
		return nil, errs.New(errs.CodeInvalidArgument, "group_by must be one of hostname, country, status").WithOp(op)
	}
	if q.Limit < 1 || q.Limit > maxTrafficLimit {
		return nil, errs.New(errs.CodeInvalidArgument, fmt.Sprintf("limit must be between 1 and %d", maxTrafficLimit)).WithOp(op)
	}
	if err := validateTimeRange(op, q.Since, q.Until, maxTrafficWindowDays*24*time.Hour); err != nil {
		return nil, err
	}
	variables := map[string]any{
		"zoneTag": client.zoneID,
		"since":   q.Since.UTC().Format(time.RFC3339),
		"until":   q.Until.UTC().Format(time.RFC3339),
		"limit":   q.Limit,
	}
	var response trafficResponse
	if err := client.graphql(ctx, fmt.Sprintf(trafficQueryTemplate, alias), variables, &response); err != nil {
		return nil, err
	}
	if len(response.Viewer.Zones) == 0 {
		return []TrafficRow{}, nil
	}
	groups := response.Viewer.Zones[0].Groups
	rows := make([]TrafficRow, 0, len(groups))
	for _, group := range groups {
		rows = append(rows, TrafficRow{
			Dimension:      dimensionString(group.Dimensions[q.GroupBy]),
			Requests:       group.Sum.Requests,
			Bytes:          group.Sum.Bytes,
			CachedRequests: group.Sum.CachedRequests,
			CachedBytes:    group.Sum.CachedBytes,
		})
	}
	return rows, nil
}

// trafficDimensionAlias maps a group_by value onto the GraphQL dimensions entry.
func trafficDimensionAlias(groupBy string) (string, bool) {
	switch groupBy {
	case "hostname":
		return "hostname: clientRequestHTTPHost", true
	case "country":
		return "country: clientCountryName", true
	case "status":
		return "status: edgeResponseStatus", true
	default:
		return "", false
	}
}

// validateTimeRange enforces the bounded, half-open window both templates use.
func validateTimeRange(op string, since, until time.Time, maxWindow time.Duration) error {
	switch {
	case since.IsZero() || until.IsZero():
		return errs.New(errs.CodeInvalidArgument, "since and until are required").WithOp(op)
	case until.Before(since):
		return errs.New(errs.CodeInvalidArgument, "until must not be before since").WithOp(op)
	case until.Sub(since) > maxWindow:
		days := int(maxWindow / (24 * time.Hour))
		return errs.New(errs.CodeInvalidArgument, fmt.Sprintf("time range must not exceed %d days", days)).WithOp(op)
	}
	return nil
}

// dimensionString renders a GraphQL dimension, a string or a JSON number.
func dimensionString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

type trafficResponse struct {
	Viewer struct {
		Zones []struct {
			Groups []trafficGroup `json:"httpRequests1hGroups"`
		} `json:"zones"`
	} `json:"viewer"`
}

type trafficGroup struct {
	Dimensions map[string]any `json:"dimensions" jsonschema:"Dimensions the report was grouped by."`
	Sum        struct {
		Requests       float64 `json:"requests" jsonschema:"Requests counted."`
		Bytes          float64 `json:"bytes" jsonschema:"Bytes transferred."`
		CachedRequests float64 `json:"cachedRequests"`
		CachedBytes    float64 `json:"cachedBytes"`
	} `json:"sum"`
}
