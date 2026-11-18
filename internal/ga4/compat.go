// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"fmt"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// rule is one curated Layer A incompatibility. A rule matches when at least one
// dimension from dimensions and one metric from metricsA are present; metricsB,
// when set, must also be present. remove names the field the error tells the
// caller to drop; empty means the matched metric.
type rule struct {
	dimensions  []string
	metricsA    []string
	metricsB    []string
	remove      string
	replacement string
	realtime    bool
}

// compatRules is the curated Layer A table. Every entry was observed against a
// live property; see docs/integrations/ga4.md §6. Layer B (live metadata) and
// the ecommerce capability probe are out of this slice.
var compatRules = []rule{
	{
		dimensions:  []string{"itemId"},
		metricsA:    []string{"eventCount", "screenPageViews", "averageSessionDuration", "bounceRate"},
		replacement: "item metrics such as itemsViewed",
	},
	{
		dimensions:  []string{"itemId", "itemName", "itemCategory", "itemBrand"},
		metricsA:    []string{"bounceRate", "averageSessionDuration"},
		replacement: "item metrics such as itemsViewed",
	},
	{
		metricsA:    []string{"ecommercePurchases"},
		metricsB:    []string{"itemRevenue"},
		remove:      "ecommercePurchases",
		replacement: "purchaseRevenue",
	},
	{
		dimensions:  []string{"transactionId"},
		metricsA:    []string{"sessions"},
		replacement: "ecommercePurchases",
	},
	{
		dimensions:  []string{"eventName"},
		metricsA:    []string{"activeUsers"},
		replacement: "eventCount",
		realtime:    true,
	},
}

// Validate reports the first curated incompatibility for a standard report, or
// nil when the combination is not known to be invalid.
func Validate(dimensions, metrics []string) error {
	return validate(dimensions, metrics, false)
}

// ValidateRealtime reports the first curated incompatibility for a realtime
// report, or nil when the combination is not known to be invalid.
func ValidateRealtime(dimensions, metrics []string) error {
	return validate(dimensions, metrics, true)
}

func validate(dimensions, metrics []string, realtime bool) error {
	dims, mets := fieldSet(dimensions), fieldSet(metrics)
	for _, candidate := range compatRules {
		if candidate.realtime != realtime {
			continue
		}
		dimension := ""
		if len(candidate.dimensions) > 0 {
			var ok bool
			if dimension, ok = firstMatch(dims, candidate.dimensions); !ok {
				continue
			}
		}
		metric, ok := firstMatch(mets, candidate.metricsA)
		if !ok {
			continue
		}
		second := ""
		if len(candidate.metricsB) > 0 {
			if second, ok = firstMatch(mets, candidate.metricsB); !ok {
				continue
			}
		}
		offending := candidate.remove
		if offending == "" {
			offending = metric
			if second != "" {
				offending = second
			}
		}
		return incompatible(dimension, metric, second, offending, candidate)
	}
	return nil
}

func incompatible(dimension, metric, second, offending string, candidate rule) error {
	pair := metric
	switch {
	case second != "":
		pair = metric + " and " + second
	case dimension != "":
		pair = dimension + " and " + metric
	}
	invalid := errs.New(errs.CodeValidationFailed,
		fmt.Sprintf("GA4 cannot query %s together; please remove %s to make the request compatible for example", pair, offending)).
		WithDetail("reason", "incompatible").
		WithDetail("offending_field", offending)
	if candidate.replacement != "" {
		_ = invalid.WithDetail("suggested_replacement", candidate.replacement)
	}
	return invalid
}

func fieldSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

func firstMatch(present map[string]struct{}, candidates []string) (string, bool) {
	for _, candidate := range candidates {
		if _, ok := present[candidate]; ok {
			return candidate, true
		}
	}
	return "", false
}
