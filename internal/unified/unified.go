// SPDX-License-Identifier: Apache-2.0

// Package unified joins data from several sources with declared semantics.
//
// It never produces a single blended "truth" number: a comparison reports each
// source separately, the ratio it computes, and the quality flags that qualify
// it (sampling, missing data, alignment). See docs/unified-analytics.md.
package unified

import (
	"context"
	"fmt"
	"sync"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// Binding is a declared Magento/Cloudflare/GA4 triple.
type Binding struct {
	ID         string
	Magento    string
	Cloudflare string
	GA4        string
}

// Validate checks that a binding names distinct, non-empty instances.
func (b Binding) Validate() error {
	if b.ID == "" {
		return fmt.Errorf("binding id is required")
	}
	if b.Magento == "" || b.Cloudflare == "" || b.GA4 == "" {
		return fmt.Errorf("binding %q must name a magento, cloudflare, and ga4 instance", b.ID)
	}
	if b.Magento == b.Cloudflare || b.Magento == b.GA4 || b.Cloudflare == b.GA4 {
		return fmt.Errorf("binding %q must reference distinct instances", b.ID)
	}
	return nil
}

// Traffic is the Cloudflare side of a comparison.
type Traffic struct {
	Requests       float64 `json:"requests" jsonschema:"Requests counted."`
	CachedRequests float64 `json:"cached_requests" jsonschema:"Requests served from cache."`
	Rows           int     `json:"rows" jsonschema:"Per-row results, in input order."`
}

// Sessions is the GA4 side of a comparison.
type Sessions struct {
	Sessions float64 `json:"sessions" jsonschema:"Sessions counted."`
	Users    float64 `json:"users" jsonschema:"Distinct users counted."`
	TimeZone string  `json:"time_zone" jsonschema:"IANA time zone the report was computed in."`
	Sampled  bool    `json:"sampled" jsonschema:"True when the answer is a sample rather than the full set."`
}

// Quality qualifies a comparison result.
type Quality struct {
	Aligned bool     `json:"aligned" jsonschema:"True when both windows cover the same length."`
	Sampled bool     `json:"sampled" jsonschema:"True when the answer is a sample rather than the full set."`
	Notes   []string `json:"notes,omitempty" jsonschema:"Free-text notes."`
}

// Comparison is the result of comparing Cloudflare requests with GA4 sessions.
type Comparison struct {
	Binding    string    `json:"binding" jsonschema:"The unified binding that was resolved."`
	From       time.Time `json:"from" jsonschema:"Start of the comparison window."`
	To         time.Time `json:"to" jsonschema:"End of the comparison window."`
	Cloudflare Traffic   `json:"cloudflare" jsonschema:"Cloudflare traffic figures for the same window."`
	GA4        Sessions  `json:"ga4" jsonschema:"GA4 figures for the same window."`
	// RequestsPerSession is nil when GA4 reported no sessions; a ratio against
	// zero would be a fabricated number.
	RequestsPerSession *float64 `json:"requests_per_session" jsonschema:"Requests per session in the window."`
	Quality            Quality  `json:"quality" jsonschema:"Data-quality flags for this result."`
}

// TrafficProvider returns Cloudflare aggregates for an instance and window.
type TrafficProvider interface {
	Traffic(ctx context.Context, instanceID string, from, to time.Time) (Traffic, error)
}

// SessionProvider returns GA4 aggregates for an instance and window.
type SessionProvider interface {
	Sessions(ctx context.Context, instanceID string, from, to time.Time) (Sessions, error)
}

// Service performs cross-source comparisons.
type Service struct {
	bindings map[string]Binding
	traffic  TrafficProvider
	sessions SessionProvider
}

// NewService validates bindings and providers.
func NewService(bindings []Binding, traffic TrafficProvider, sessions SessionProvider) (*Service, error) {
	if traffic == nil || sessions == nil {
		return nil, fmt.Errorf("unified: both providers are required")
	}
	index := make(map[string]Binding, len(bindings))
	for _, b := range bindings {
		if err := b.Validate(); err != nil {
			return nil, fmt.Errorf("unified: %w", err)
		}
		if _, exists := index[b.ID]; exists {
			return nil, fmt.Errorf("unified: duplicate binding %q", b.ID)
		}
		index[b.ID] = b
	}
	return &Service{bindings: index, traffic: traffic, sessions: sessions}, nil
}

// Bindings returns the configured bindings.
func (s *Service) Bindings() []Binding {
	out := make([]Binding, 0, len(s.bindings))
	for _, b := range s.bindings {
		out = append(out, b)
	}
	return out
}

const maxWindow = 31 * 24 * time.Hour

// Compare runs one cross-source comparison over a UTC window.
func (s *Service) Compare(ctx context.Context, bindingID string, from, to time.Time) (*Comparison, error) {
	binding, ok := s.bindings[bindingID]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "unknown binding").WithDetail("binding", bindingID)
	}
	from, to = from.UTC(), to.UTC()
	if !from.Before(to) {
		return nil, errs.New(errs.CodeInvalidArgument, "window end must be after start")
	}
	if to.Sub(from) > maxWindow {
		return nil, errs.New(errs.CodeInvalidArgument, "window must not exceed 31 days")
	}

	var (
		traffic                 Traffic
		sessions                Sessions
		trafficErr, sessionsErr error
		wg                      sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		traffic, trafficErr = s.traffic.Traffic(ctx, binding.Cloudflare, from, to)
	}()
	go func() {
		defer wg.Done()
		sessions, sessionsErr = s.sessions.Sessions(ctx, binding.GA4, from, to)
	}()
	wg.Wait()
	if trafficErr != nil {
		return nil, trafficErr
	}
	if sessionsErr != nil {
		return nil, sessionsErr
	}

	result := &Comparison{
		Binding: binding.ID, From: from, To: to,
		Cloudflare: traffic, GA4: sessions,
		Quality: Quality{Aligned: true, Sampled: sessions.Sampled},
	}
	switch {
	case traffic.Requests == 0:
		result.Quality.Notes = append(result.Quality.Notes, "cloudflare_no_requests_in_window")
	case sessions.Sessions == 0:
		result.Quality.Notes = append(result.Quality.Notes, "ga4_no_sessions_in_window")
	default:
		ratio := traffic.Requests / sessions.Sessions
		result.RequestsPerSession = &ratio
	}
	if sessions.Sampled {
		result.Quality.Notes = append(result.Quality.Notes, "ga4_sampled")
	}
	result.Quality.Notes = append(result.Quality.Notes,
		"requests_are_not_sessions: the ratio reflects asset and bot mix as much as audience")
	if sessions.TimeZone != "" {
		result.Quality.Notes = append(result.Quality.Notes, "ga4_time_zone="+sessions.TimeZone+"; window is UTC")
	}
	return result, nil
}
