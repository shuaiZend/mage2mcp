// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestCreateProductOptionFallsBackToProductUpdate(t *testing.T) {
	var putBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/options"):
			http.NotFound(w, r)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/products/SKU-1"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &putBody)
			_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/options"):
			_, _ = w.Write([]byte(`[{"option_id":5,"title":"For","type":"drop_down","is_require":true,"values":[]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	option, err := svc.CreateProductOption(context.Background(), "SKU-1", CustomOptionInput{
		Title: "For", Type: "drop_down", IsRequire: true,
		Values: []CustomOptionValueInput{{Title: "Honor Magic 9", Price: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if option.OptionID != 5 || option.Title != "For" {
		t.Fatalf("option = %+v", option)
	}
	if putBody["product"] == nil {
		t.Fatalf("fallback body = %+v", putBody)
	}
}

// MAG-16: when the fallback product update succeeds but the product does not
// list the option, the old code returned a synthetic ProductOption built from
// the request — a success value for an option that does not exist. It must
// report the unconfirmed state instead.
func TestCreateProductOptionDoesNotFabricateOnMissingReadBack(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/options"):
			http.NotFound(w, r)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/products/SKU-1"):
			_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/options"):
			// The product reports no options at all.
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	option, err := svc.CreateProductOption(context.Background(), "SKU-1", CustomOptionInput{
		Title: "For", Type: "drop_down", IsRequire: true,
		Values: []CustomOptionValueInput{{Title: "Honor Magic 9", Price: 0}},
	})
	if err == nil {
		t.Fatalf("CreateProductOption fabricated a success: %+v", option)
	}
	if option != nil {
		t.Fatalf("option = %+v, want nil alongside the error", option)
	}
	if code := errs.CodeOf(err); code != errs.CodeConflict {
		t.Fatalf("code = %s, want conflict", code)
	}
}

// A read-back that fails is also not a success.
func TestCreateProductOptionReadBackFailureIsNotSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/options"):
			http.NotFound(w, r)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/products/SKU-1"):
			_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/options"):
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	option, err := svc.CreateProductOption(context.Background(), "SKU-1", CustomOptionInput{
		Title: "For", Type: "drop_down", IsRequire: true,
		Values: []CustomOptionValueInput{{Title: "Honor Magic 9", Price: 0}},
	})
	if err == nil {
		t.Fatalf("CreateProductOption returned %+v with no error despite an unreadable read-back", option)
	}
}
