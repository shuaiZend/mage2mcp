// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestProductOptions(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"option_id":1,"title":"Gift wrap","type":"checkbox","is_require":false,"sort_order":1,
			"values":[{"option_type_id":2,"title":"Yes","price":2.5,"price_type":"fixed","sku":"GIFT","sort_order":1}]}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	options, err := svc.ProductOptions(context.Background(), "SKU-1")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/products/SKU-1/options" {
		t.Fatalf("path = %s", path)
	}
	if len(options) != 1 || options[0].Title != "Gift wrap" || len(options[0].Values) != 1 || options[0].Values[0].Price != 2.5 {
		t.Fatalf("options = %+v", options)
	}
	if _, err := svc.ProductOptions(context.Background(), "bad sku!"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}
