// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"time"
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
