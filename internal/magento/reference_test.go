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
		case "/rest/default/V1/customerGroups/search":
			_, _ = w.Write([]byte(`{"items":[{"id":0,"code":"NOT LOGGED IN","tax_class_id":3},{"id":1,"code":"General","tax_class_id":3}],"total_count":2}`))
		case "/rest/default/V1/taxClasses/search":
			_, _ = w.Write([]byte(`{"items":[{"class_id":2,"class_name":"Taxable Goods","class_type":"PRODUCT"}],"total_count":1}`))
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

func TestStoreConfigsAndFlatCategories(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/store/storeConfigs":
			_, _ = w.Write([]byte(`[{"id":1,"code":"default","website_id":1,"locale":"en_US","base_currency_code":"USD","timezone":"Asia/Shanghai","base_url":"https://shop.example.com/"}]`))
		case "/rest/default/V1/categories":
			_, _ = w.Write([]byte(`{"id":1,"parent_id":0,"name":"Root","level":0,"children_data":[
				{"id":2,"parent_id":1,"name":"Default Category","level":1,"children_data":[
					{"id":10,"parent_id":2,"name":"Phones","level":2,"children_data":[
						{"id":11,"parent_id":10,"name":"Android","level":3,"children_data":[]}]}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	stores, err := svc.StoreConfigs(context.Background())
	if err != nil || len(stores) != 1 || stores[0].Timezone != "Asia/Shanghai" || stores[0].BaseCurrencyCode != "USD" {
		t.Fatalf("stores = %+v err=%v", stores, err)
	}
	cats, err := svc.FlatCategories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 4 {
		t.Fatalf("cats = %+v", cats)
	}
	if cats[3].Path != "Root/Default Category/Phones/Android" || cats[3].Level != 3 || cats[3].ParentID != 10 {
		t.Fatalf("deepest = %+v", cats[3])
	}
}
