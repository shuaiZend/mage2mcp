// Command fakemagento is a minimal Magento 2 REST API used by the end-to-end
// test script. It stores products in memory and implements only the endpoints
// the gateway touches; it is not a Magento emulator.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	mu       sync.Mutex
	products = map[string]map[string]string{}
)

func productJSON(sku string) map[string]any {
	mu.Lock()
	defer mu.Unlock()
	p := products[sku]
	attrs := make([]map[string]string, 0, len(p))
	for code, value := range p {
		attrs = append(attrs, map[string]string{"attribute_code": code, "value": value})
	}
	return map[string]any{
		"id": 1, "sku": sku, "name": p["name"], "type_id": "simple", "price": 9.99,
		"attribute_set_id": 4, "custom_attributes": attrs,
	}
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	raw, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}

func main() {
	port := "8090"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}
	mux := http.NewServeMux()
	productPath := regexp.MustCompile(`^/rest/default/V1/products/([^/]+)$`)
	mux.HandleFunc("/rest/default/V1/products", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			items := make([]map[string]any, 0, len(products))
			for sku := range products {
				items = append(items, productJSON(sku))
			}
			mu.Unlock()
			writeJSON(w, 200, map[string]any{"items": items, "total_count": len(items)})
		case http.MethodPost:
			var body struct {
				Product struct {
					SKU              string `json:"sku"`
					Name             string `json:"name"`
					CustomAttributes []struct {
						Code  string `json:"attribute_code"`
						Value any    `json:"value"`
					} `json:"custom_attributes"`
				} `json:"product"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			attrs := map[string]string{"name": body.Product.Name}
			for _, attr := range body.Product.CustomAttributes {
				attrs[attr.Code] = fmt.Sprint(attr.Value)
			}
			products[body.Product.SKU] = attrs
			mu.Unlock()
			writeJSON(w, 200, productJSON(body.Product.SKU))
		default:
			writeJSON(w, 405, map[string]string{"message": "method"})
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if m := productPath.FindStringSubmatch(path); m != nil {
			sku := m[1]
			switch r.Method {
			case http.MethodGet:
				mu.Lock()
				_, ok := products[sku]
				mu.Unlock()
				if !ok {
					writeJSON(w, 404, map[string]string{"message": "not found"})
					return
				}
				writeJSON(w, 200, productJSON(sku))
			case http.MethodPut:
				var body struct {
					Product struct {
						CustomAttributes []struct {
							Code  string `json:"attribute_code"`
							Value any    `json:"value"`
						} `json:"custom_attributes"`
					} `json:"product"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				if products[sku] == nil {
					products[sku] = map[string]string{}
				}
				for _, attr := range body.Product.CustomAttributes {
					products[sku][attr.Code] = fmt.Sprint(attr.Value)
				}
				mu.Unlock()
				writeJSON(w, 200, productJSON(sku))
			case http.MethodDelete:
				writeJSON(w, 200, true)
			}
			return
		}
		if strings.HasSuffix(path, "/media") && r.Method == http.MethodGet {
			writeJSON(w, 200, []any{})
			return
		}
		if strings.Contains(path, "/stockItems/") {
			writeJSON(w, 200, map[string]any{"qty": 5, "is_in_stock": true, "manage_stock": true})
			return
		}
		if strings.Contains(path, "/children") && r.Method == http.MethodGet {
			writeJSON(w, 200, []any{})
			return
		}
		if strings.HasPrefix(path, "/rest/default/V1/categories") {
			writeJSON(w, 200, map[string]any{"id": 2, "name": "Default", "children_data": []any{}})
			return
		}
		if strings.HasPrefix(path, "/rest/default/V1/orders/") && strings.HasSuffix(path, "/comments") {
			writeJSON(w, 200, []any{})
			return
		}
		if strings.HasPrefix(path, "/rest/default/V1/orders/") {
			writeJSON(w, 200, map[string]any{"entity_id": 1, "increment_id": "000000001", "status": "processing",
				"grand_total": 19.98, "total_qty_ordered": 2, "order_currency_code": "USD",
				"items": []map[string]any{{"item_id": 1, "sku": "SKU-1", "name": "Widget", "qty_ordered": 2, "price": 9.99, "row_total": 19.98, "product_type": "simple"}}})
			return
		}
		if strings.Contains(path, "/options") {
			writeJSON(w, 200, []map[string]string{{"label": "Red", "value": "12"}})
			return
		}
		if strings.HasPrefix(path, "/rest/default/V1/customers/") {
			writeJSON(w, 200, map[string]any{"id": 7, "group_id": 1, "website_id": 1, "store_id": 1, "addresses": []any{}})
			return
		}
		if strings.Contains(path, "/inventory/source-items") {
			writeJSON(w, 200, []any{})
			return
		}
		writeJSON(w, 404, map[string]string{"message": "unhandled " + r.Method + " " + path})
	})
	log.Printf("fake magento listening on 127.0.0.1:%s", port) // #nosec G706 -- port is a local test flag
	server := &http.Server{
		Addr:              "127.0.0.1:" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(server.ListenAndServe()) // #nosec G114 -- test server with a read-header timeout
}
