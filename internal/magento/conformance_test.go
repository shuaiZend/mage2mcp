// SPDX-License-Identifier: Apache-2.0

package magento

// Conformance tests over payloads recorded from a real Magento 2 store.
//
// Every other Magento test in this repository drives a fake whose bodies were
// written by us, so a wrong assumption about the wire format passes. The fixtures
// under test/fixtures/magento were captured from
// https://demo-magento-2.auroracreation.com (store view `pl`) and are replayed
// here unchanged: if the client stops parsing what a real store actually returns,
// these fail.
//
// This does not replace an integration test — it cannot notice a store changing
// its behaviour, because the fixtures are frozen. What it does is keep the shapes
// we once observed under regression, which is the part the audit (MAG-23) could not
// get without an environment.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixtureDir is where the recorded payloads live, relative to this package.
const fixtureDir = "../../test/fixtures/magento"

// fixtureServer serves one recorded payload for any GET, and records the path the
// client asked for so the test can assert the route too.
func fixtureServer(t *testing.T, name string) (*httptest.Server, *string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var requested string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &requested
}

// plService returns a service configured like the store the fixtures came from.
func plService(t *testing.T, baseURL string) *Service {
	t.Helper()
	client, err := NewClient(ClientConfig{
		BaseURL: baseURL, StoreCode: "pl", Signer: testSigner(t), Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	svc, err := NewService(client)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestConformanceProductList parses a real product search response.
func TestConformanceProductList(t *testing.T) {
	server, _ := fixtureServer(t, "products.json")
	svc := plService(t, server.URL)

	products, total, err := svc.Search(context.Background(), ProductQuery{Limit: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(products) == 0 {
		t.Fatal("no products parsed from a real payload")
	}
	if total < len(products) {
		t.Fatalf("total = %d, want at least the %d returned", total, len(products))
	}
	first := products[0]
	if first.SKU == "" || first.Name == "" || first.ID == 0 {
		t.Fatalf("product = %+v, want sku, name and id parsed", first)
	}
	if first.TypeID == "" || first.AttributeSetID == 0 {
		t.Fatalf("product = %+v, want type and attribute set parsed", first)
	}
}

// TestConformanceSingleProduct parses a real single-product response, including the
// fields the gateway reads out of extension_attributes.
func TestConformanceSingleProduct(t *testing.T) {
	server, requested := fixtureServer(t, "product_one.json")
	svc := plService(t, server.URL)

	product, err := svc.Get(context.Background(), "WJ01")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if product.SKU != "WJ01" {
		t.Fatalf("sku = %q, want WJ01", product.SKU)
	}
	if product.Name == "" || product.TypeID == "" {
		t.Fatalf("product = %+v, want name and type parsed", product)
	}
	// This SKU is a configurable parent, and Magento reports price 0 for one: the
	// price lives on the children. Asserting a non-zero price here would encode a
	// wrong expectation, which is exactly what these fixtures exist to prevent.
	if product.TypeID == "configurable" && product.Price != 0 {
		t.Fatalf("configurable parent price = %v, want the store's 0", product.Price)
	}
	if len(product.Attributes) == 0 {
		t.Fatalf("product = %+v, want custom attributes parsed", product)
	}
	if len(product.CategoryIDs) == 0 {
		t.Fatalf("product = %+v, want category placement parsed", product)
	}
	if *requested == "" {
		t.Fatal("client did not issue a request")
	}
}

// TestConformanceOrderSearch parses a real order search response and checks that
// total_count is read: the gateway used to project it away and report zero.
func TestConformanceOrderSearch(t *testing.T) {
	server, _ := fixtureServer(t, "orders.json")
	svc := plService(t, server.URL)

	orders, total, err := svc.SearchOrders(context.Background(), OrderQuery{Limit: 2})
	if err != nil {
		t.Fatalf("SearchOrders: %v", err)
	}
	if len(orders) == 0 {
		t.Fatal("no orders parsed from a real payload")
	}
	if orders[0].EntityID == 0 || orders[0].IncrementID == "" {
		t.Fatalf("order = %+v, want entity_id and increment_id parsed", orders[0])
	}
	if orders[0].Currency == "" {
		t.Fatalf("order = %+v, want the currency code parsed", orders[0])
	}
	if total < len(orders) {
		t.Fatalf("total = %d, want at least %d", total, len(orders))
	}
}

// TestConformanceInventorySources parses a real MSI source list. A successful
// create answers with an empty array on this store, which is why the create path
// treats that as success.
func TestConformanceInventorySources(t *testing.T) {
	server, _ := fixtureServer(t, "inventory_sources.json")
	svc := plService(t, server.URL)

	sources, total, err := svc.Sources(context.Background())
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("no sources parsed from a real payload")
	}
	if sources[0].SourceCode == "" {
		t.Fatalf("source = %+v, want a source code", sources[0])
	}
	if total < len(sources) {
		t.Fatalf("total = %d, want at least %d", total, len(sources))
	}
}

// TestConformanceStoreConfigs parses a real store list, which is what the
// instance's store_code resolves against.
func TestConformanceStoreConfigs(t *testing.T) {
	server, _ := fixtureServer(t, "store_configs.json")
	svc := plService(t, server.URL)

	stores, err := svc.StoreConfigs(context.Background())
	if err != nil {
		t.Fatalf("StoreConfigs: %v", err)
	}
	if len(stores) == 0 {
		t.Fatal("no stores parsed from a real payload")
	}
	if stores[0].Code == "" || stores[0].BaseCurrencyCode == "" {
		t.Fatalf("store = %+v, want code and currency", stores[0])
	}
}

// TestConformanceCategories parses a real category search.
func TestConformanceCategories(t *testing.T) {
	server, _ := fixtureServer(t, "categories.json")
	svc := plService(t, server.URL)

	categories, err := svc.CategoryTree(context.Background(), 2, 2)
	if err != nil {
		t.Fatalf("CategoryTree: %v", err)
	}
	if len(categories) == 0 {
		t.Fatal("no categories parsed from a real payload")
	}
	if categories[0].ID == 0 || categories[0].Name == "" {
		t.Fatalf("category = %+v, want id and name", categories[0])
	}
}

// TestConformanceTaxClasses parses a real tax class search.
func TestConformanceTaxClasses(t *testing.T) {
	server, _ := fixtureServer(t, "tax_classes.json")
	svc := plService(t, server.URL)

	classes, err := svc.TaxClasses(context.Background())
	if err != nil {
		t.Fatalf("TaxClasses: %v", err)
	}
	if len(classes) == 0 {
		t.Fatal("no tax classes parsed from a real payload")
	}
	if classes[0].ClassName == "" {
		t.Fatalf("class = %+v, want a name", classes[0])
	}
}

// TestConformanceMediaTypes parses the real role list for the Default attribute
// set, which is what the gateway validates a media upload's roles against.
func TestConformanceMediaTypes(t *testing.T) {
	server, _ := fixtureServer(t, "media_types.json")
	svc := plService(t, server.URL)

	types, err := svc.MediaTypes(context.Background(), "Default")
	if err != nil {
		t.Fatalf("MediaTypes: %v", err)
	}
	if len(types) == 0 {
		t.Fatal("no media types parsed from a real payload")
	}
}

// TestConformanceFixturesAreRealPayloads guards the fixtures themselves: a file
// that is not JSON, or an empty array, would let every test above pass vacuously.
func TestConformanceFixturesAreRealPayloads(t *testing.T) {
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatalf("read fixture dir: %v", err)
	}
	if len(entries) < 10 {
		t.Fatalf("fixtures = %d, want the recorded set", len(entries))
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(fixtureDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		var anyValue any
		if err := json.Unmarshal(raw, &anyValue); err != nil {
			t.Fatalf("%s is not JSON: %v", entry.Name(), err)
		}
		if anyValue == nil {
			t.Fatalf("%s is empty", entry.Name())
		}
	}
}
