// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/shuaiZend/mage2mcp/internal/magento"
)

func TestImportBatchResultKeepsAllFailures(t *testing.T) {
	result := &magento.ImportResult{
		Total: 3, Created: 1, Failed: 2,
		Rows: []magento.ImportResultRow{
			{RowNumber: 2, SKU: "A", Action: "create", Status: "succeeded"},
			{RowNumber: 3, SKU: "B", Action: "create", Status: "failed", Error: "boom"},
			{RowNumber: 4, SKU: "C", Action: "update", Status: "failed", Error: "boom"},
		},
	}
	batch := importBatchResult(result)
	if batch.Total != 3 || batch.Processed != 3 || batch.Failed != 2 || len(batch.Failures) != 2 {
		t.Fatalf("batch = %+v", batch)
	}
	if batch.Failures[1].RowNumber != 4 || batch.Failures[1].SKU != "C" {
		t.Fatalf("failures = %+v", batch.Failures)
	}
}
