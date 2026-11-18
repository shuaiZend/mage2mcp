// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestValidateRejectsKnownIncompatibilities(t *testing.T) {
	cases := []struct {
		name       string
		dimensions []string
		metrics    []string
		offending  string
	}{
		{"itemId with eventCount", []string{"itemId"}, []string{"eventCount"}, "eventCount"},
		{"itemId with screenPageViews", []string{"itemId"}, []string{"screenPageViews"}, "screenPageViews"},
		{"itemId with averageSessionDuration", []string{"itemId"}, []string{"averageSessionDuration"}, "averageSessionDuration"},
		{"itemId with bounceRate", []string{"itemId"}, []string{"bounceRate"}, "bounceRate"},
		{"itemName with bounceRate", []string{"itemName"}, []string{"bounceRate"}, "bounceRate"},
		{"itemCategory with averageSessionDuration", []string{"itemCategory"}, []string{"averageSessionDuration"}, "averageSessionDuration"},
		{"itemBrand with bounceRate", []string{"itemBrand"}, []string{"bounceRate"}, "bounceRate"},
		{"ecommercePurchases with itemRevenue", nil, []string{"ecommercePurchases", "itemRevenue"}, "ecommercePurchases"},
		{"transactionId with sessions", []string{"transactionId"}, []string{"sessions"}, "sessions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.dimensions, tc.metrics)
			if err == nil {
				t.Fatal("expected an incompatibility error")
			}
			if got := errs.CodeOf(err); got != errs.CodeValidationFailed {
				t.Fatalf("code = %s, want %s", got, errs.CodeValidationFailed)
			}
			if got := errorDetail(t, err, "reason"); got != "incompatible" {
				t.Fatalf("reason = %q", got)
			}
			if got := errorDetail(t, err, "offending_field"); got != tc.offending {
				t.Fatalf("offending_field = %q, want %q", got, tc.offending)
			}
			if !strings.Contains(err.Error(), "remove "+tc.offending) {
				t.Fatalf("message must name the field to remove: %v", err)
			}
		})
	}
}

func TestValidateAcceptsCompatibleCombinations(t *testing.T) {
	cases := []struct {
		name       string
		dimensions []string
		metrics    []string
	}{
		{"itemId with sessions", []string{"itemId"}, []string{"sessions"}},
		{"itemId with totalUsers", []string{"itemId"}, []string{"totalUsers"}},
		{"ecommercePurchases with eventCount", nil, []string{"ecommercePurchases", "eventCount"}},
		{"purchase revenue bundle", []string{"date"}, []string{"ecommercePurchases", "purchaseRevenue"}},
		{"source and medium with eventCount", []string{"sessionSource", "sessionMedium"}, []string{"eventCount"}},
		{"eventName with sessions", []string{"eventName"}, []string{"sessions"}},
		{"realtime rule does not apply to standard reports", []string{"eventName"}, []string{"activeUsers"}},
		{"traffic report", []string{"date"}, []string{"sessions", "totalUsers"}},
		{"empty request", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.dimensions, tc.metrics); err != nil {
				t.Fatalf("Validate(%v, %v) = %v", tc.dimensions, tc.metrics, err)
			}
		})
	}
}

func TestValidateRealtime(t *testing.T) {
	if err := ValidateRealtime([]string{"eventName"}, []string{"activeUsers"}); err == nil {
		t.Fatal("expected the realtime eventName/activeUsers rule to fire")
	} else if got := errorDetail(t, err, "offending_field"); got != "activeUsers" {
		t.Fatalf("offending_field = %q", got)
	}
	valid := []struct {
		dimensions []string
		metrics    []string
	}{
		{[]string{"unifiedScreenName"}, []string{"activeUsers"}},
		{[]string{"eventName"}, []string{"eventCount"}},
		{[]string{"date"}, []string{"sessions"}},
	}
	for _, tc := range valid {
		if err := ValidateRealtime(tc.dimensions, tc.metrics); err != nil {
			t.Fatalf("ValidateRealtime(%v, %v) = %v", tc.dimensions, tc.metrics, err)
		}
	}
	// The realtime rule must not leak into the standard validator.
	if err := Validate([]string{"eventName"}, []string{"activeUsers"}); err != nil {
		t.Fatalf("Validate applied a realtime-only rule: %v", err)
	}
}
