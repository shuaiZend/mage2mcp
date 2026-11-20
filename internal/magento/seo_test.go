// SPDX-License-Identifier: Apache-2.0

package magento

import "testing"

func TestSEOScoreIgnoresOptionalChecks(t *testing.T) {
	audit := &SEOAudit{Checks: []SEOCheck{
		{Field: "description", Status: "pass"},
		{Field: "meta_keywords", Status: "warn", Optional: true},
	}}
	scoreSEO(audit)
	if audit.Score != 100 {
		t.Fatalf("score = %d, want 100", audit.Score)
	}
}
