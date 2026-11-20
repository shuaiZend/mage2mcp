// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCustomerGroupsAndTaxClasses(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/customerGroups":
			_, _ = w.Write([]byte(`[{"id":0,"code":"NOT LOGGED IN","tax_class_id":3},{"id":1,"code":"General","tax_class_id":3}]`))
		case "/rest/default/V1/taxClasses":
			_, _ = w.Write([]byte(`[{"class_id":2,"class_name":"Taxable Goods","class_type":"PRODUCT"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	groups, err := svc.CustomerGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[1].Code != "General" || groups[1].TaxClassID != 3 {
		t.Fatalf("groups = %+v", groups)
	}
	classes, err := svc.TaxClasses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(classes) != 1 || classes[0].ClassID != 2 || classes[0].ClassType != "PRODUCT" {
		t.Fatalf("classes = %+v", classes)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v", paths)
	}
}
