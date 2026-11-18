// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	// maxSecurityWindowDays is the Security Events query ceiling on every plan.
	maxSecurityWindowDays = 30
	// securityRetentionDays is the documented retention of Security Events.
	securityRetentionDays = 31
)

// securityQuery is the fixed document behind cloudflare.security.summary.
// It counts security events, not requests, so it is never additive with traffic.
const securityQuery = `query ZoneSecuritySummary($zoneTag: string, $since: Time, $until: Time) {
  viewer {
    zones(filter: { zoneTag: $zoneTag }) {
      firewallEventsAdaptiveGroups(limit: 100, filter: { datetime_geq: $since, datetime_lt: $until }, orderBy: [count_DESC]) {
        count
        dimensions { action }
      }
    }
  }
}`

// SecuritySummary is the sampled security-event count per action for a window.
type SecuritySummary struct {
	// Window is the queried half-open interval, "since/until" in RFC 3339.
	Window string
	// Actions maps a Cloudflare action value to its event count.
	Actions map[string]int
	// Total is the sum of Actions; it counts events, not requests.
	Total int
	// Sampled reports that the adaptive dataset is sampled, so counts are
	// estimates that must be disclosed as such.
	Sampled bool
	// RetentionDays is the documented Security Events retention.
	RetentionDays int
}

// Summary returns security-event counts by action for the half-open window.
func (s *Service) Summary(ctx context.Context, since, until time.Time) (*SecuritySummary, error) {
	const op = "cloudflare.security.summary"
	client, err := s.clientOrError(op)
	if err != nil {
		return nil, err
	}
	if err := validateTimeRange(op, since, until, maxSecurityWindowDays*24*time.Hour); err != nil {
		return nil, err
	}
	variables := map[string]any{
		"zoneTag": client.zoneID,
		"since":   since.UTC().Format(time.RFC3339),
		"until":   until.UTC().Format(time.RFC3339),
	}
	var response securityResponse
	if err := client.graphql(ctx, securityQuery, variables, &response); err != nil {
		return nil, err
	}
	actions := make(map[string]int)
	total := 0
	if len(response.Viewer.Zones) > 0 {
		for _, group := range response.Viewer.Zones[0].Groups {
			action := dimensionString(group.Dimensions["action"])
			if action == "" {
				continue
			}
			actions[action] += group.Count
			total += group.Count
		}
	}
	return &SecuritySummary{
		Window:        since.UTC().Format(time.RFC3339) + "/" + until.UTC().Format(time.RFC3339),
		Actions:       actions,
		Total:         total,
		Sampled:       true,
		RetentionDays: securityRetentionDays,
	}, nil
}

type securityResponse struct {
	Viewer struct {
		Zones []struct {
			Groups []struct {
				Count      int            `json:"count"`
				Dimensions map[string]any `json:"dimensions"`
			} `json:"firewallEventsAdaptiveGroups"`
		} `json:"zones"`
	} `json:"viewer"`
}

// securityEventsQuery returns individual security events. Client IPs are not
// requested: event-level IPs are restricted data and belong behind a pii grant.
const securityEventsQuery = `query ZoneSecurityEvents($zoneTag: string, $since: Time, $until: Time, $limit: uint64) {
  viewer {
    zones(filter: { zoneTag: $zoneTag }) {
      firewallEventsAdaptive(limit: $limit, filter: { datetime_geq: $since, datetime_lt: $until }, orderBy: [datetime_DESC]) {
        datetime
        action
        ruleId
        clientCountryName
        clientRequestPath
        userAgent
      }
    }
  }
}`

// SecurityEvent is one sampled security event.
type SecurityEvent struct {
	Time      string `json:"time"`
	Action    string `json:"action"`
	RuleID    string `json:"rule_id,omitempty"`
	Country   string `json:"country,omitempty"`
	Path      string `json:"path,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
}

// SecurityEventPage is a bounded page of security events.
type SecurityEventPage struct {
	Events        []SecurityEvent `json:"events"`
	Sampled       bool            `json:"sampled"`
	RetentionDays int             `json:"retention_days"`
	Truncated     bool            `json:"truncated"`
}

type securityEventsResponse struct {
	Viewer struct {
		Zones []struct {
			Events []struct {
				DateTime          string `json:"datetime"`
				Action            string `json:"action"`
				RuleID            string `json:"ruleId"`
				ClientCountryName string `json:"clientCountryName"`
				ClientRequestPath string `json:"clientRequestPath"`
				UserAgent         string `json:"userAgent"`
			} `json:"firewallEventsAdaptive"`
		} `json:"zones"`
	} `json:"viewer"`
}

// Events returns a bounded page of security events for the half-open window.
func (s *Service) Events(ctx context.Context, since, until time.Time, limit int) (*SecurityEventPage, error) {
	const op = "cloudflare.security.events"
	client, err := s.clientOrError(op)
	if err != nil {
		return nil, err
	}
	if err := validateTimeRange(op, since, until, maxSecurityWindowDays*24*time.Hour); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, errs.New(errs.CodeInvalidArgument, "limit must be within 1..1000").WithOp(op)
	}
	variables := map[string]any{
		"zoneTag": client.zoneID,
		"since":   since.UTC().Format(time.RFC3339),
		"until":   until.UTC().Format(time.RFC3339),
		"limit":   limit,
	}
	var response securityEventsResponse
	if err := client.graphql(ctx, securityEventsQuery, variables, &response); err != nil {
		return nil, err
	}
	page := &SecurityEventPage{Sampled: true, RetentionDays: securityRetentionDays}
	if len(response.Viewer.Zones) > 0 {
		for _, event := range response.Viewer.Zones[0].Events {
			page.Events = append(page.Events, SecurityEvent{
				Time: event.DateTime, Action: event.Action, RuleID: event.RuleID,
				Country: event.ClientCountryName, Path: truncateString(event.ClientRequestPath, 512),
				UserAgent: truncateString(event.UserAgent, 200),
			})
		}
	}
	page.Truncated = len(page.Events) == limit
	return page, nil
}

func truncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
