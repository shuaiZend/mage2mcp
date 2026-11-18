// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newImportFake serves GET 404 for NEW-*, GET 200 for OLD-*, POST 200, PUT 200,
// and 500 for FAIL-*.
func newImportFake(t *testing.T, created, updated *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sku := ""
		if parts := strings.Split(r.URL.Path, "/products/"); len(parts) == 2 {
			sku = parts[1]
		}
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(sku, "OLD-"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1,"sku":"OLD-1","name":"Old","type_id":"simple"}`))
		case r.Method == http.MethodGet:
			http.NotFound(w, r)
		case r.Method == http.MethodPost:
			if strings.Contains(r.URL.Path, "FAIL") {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			*created++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":2,"sku":"NEW-1","name":"New","type_id":"simple"}`))
		case r.Method == http.MethodPut:
			*updated++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1,"sku":"OLD-1","name":"Updated","type_id":"simple"}`))
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
}

func newImportServiceAgainst(t *testing.T, baseURL string) *Service {
	t.Helper()
	client, err := NewClient(ClientConfig{BaseURL: baseURL, StoreCode: "default", Signer: &BearerSigner{Token: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(client)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
