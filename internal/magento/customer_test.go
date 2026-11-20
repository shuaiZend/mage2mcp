// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestGetCustomerOmitsPII(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7,"group_id":1,"website_id":1,"store_id":1,"created_in":"Default Store View",
			"created_at":"2026-01-02 03:04:05","updated_at":"2026-02-03 04:05:06",
			"email":"buyer@example.com","firstname":"Ada","lastname":"Lovelace",
			"addresses":[{"city":"Shanghai","street":["Secret Road 1"]}]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	customer, err := svc.GetCustomer(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/customers/7" {
		t.Fatalf("path = %q", path)
	}
	if customer.ID != 7 || customer.GroupID != 1 || customer.CreatedIn != "Default Store View" {
		t.Fatalf("customer = %+v", customer)
	}
	raw, _ := json.Marshal(customer)
	for _, forbidden := range []string{"buyer@example.com", "Ada", "Lovelace", "Shanghai", "Secret Road"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("projection leaks %q: %s", forbidden, raw)
		}
	}
}

func TestSearchCustomersByEmail(t *testing.T) {
	var query url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":7,"group_id":1,"website_id":1,"store_id":1}],"total_count":1}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	customers, total, err := svc.SearchCustomers(context.Background(), "buyer@example.com", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(customers) != 1 || customers[0].ID != 7 {
		t.Fatalf("customers = %+v total=%d", customers, total)
	}
	if got := query.Get("searchCriteria[filter_groups][0][filters][0][field]"); got != "email" {
		t.Fatalf("filter field = %q", got)
	}
	if got := query.Get("searchCriteria[filter_groups][0][filters][0][value]"); got != "buyer@example.com" {
		t.Fatalf("filter value = %q", got)
	}
	fields := query.Get("fields")
	if strings.Contains(fields, "email") || strings.Contains(fields, "firstname") {
		t.Fatalf("fields request PII: %q", fields)
	}
}

func TestCustomerValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.GetCustomer(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("id err = %v", err)
	}
	if _, _, err := svc.SearchCustomers(ctx, "", 10, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty email err = %v", err)
	}
	if _, _, err := svc.SearchCustomers(ctx, "not-an-email", 10, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("bad email err = %v", err)
	}
	if _, _, err := svc.SearchCustomers(ctx, "buyer@example.com", 500, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("limit err = %v", err)
	}
	if _, _, err := svc.SearchCustomers(ctx, "buyer@example.com", 10, 3); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("cursor err = %v", err)
	}
}

func TestCustomerAddressesOmitsPII(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7,"addresses":[
			{"id":1,"country_id":"CN","region":"Shanghai","region_id":1,"default_billing":true,"default_shipping":false,
			 "firstname":"Ada","lastname":"Lovelace","street":["Secret Road 1"],"city":"Shanghai","postcode":"200000","telephone":"123"},
			{"id":2,"country_id":"US","region":"California","region_id":12,"default_billing":false,"default_shipping":true}
		]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	addresses, err := svc.CustomerAddresses(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 2 || addresses[0].CountryID != "CN" || !addresses[0].DefaultBilling || !addresses[1].DefaultShipping {
		t.Fatalf("addresses = %+v", addresses)
	}
	raw, _ := json.Marshal(addresses)
	for _, forbidden := range []string{"Secret Road", "200000", "telephone", "Ada", "Lovelace"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("projection leaks %q: %s", forbidden, raw)
		}
	}
	if _, err := svc.CustomerAddresses(context.Background(), 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}

func TestCreateCustomerSendsPIIButReturnsProjection(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":9,"group_id":1,"website_id":1,"store_id":1,"created_at":"2026-11-19 10:00:00",
			"email":"new@example.com","firstname":"Ada","lastname":"Lovelace"}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	customer, err := svc.CreateCustomer(context.Background(), CustomerCreateInput{
		Email: "new@example.com", Firstname: "Ada", Lastname: "Lovelace", WebsiteID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := body["customer"].(map[string]any)
	if payload["email"] != "new@example.com" || payload["firstname"] != "Ada" || payload["website_id"] != float64(1) {
		t.Fatalf("body = %+v", payload)
	}
	raw, _ := json.Marshal(customer)
	for _, forbidden := range []string{"new@example.com", "Ada", "Lovelace"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("projection leaks %q: %s", forbidden, raw)
		}
	}
	if customer.ID != 9 || customer.WebsiteID != 1 {
		t.Fatalf("customer = %+v", customer)
	}
}

func TestUpdateCustomerPartial(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":9,"group_id":2,"website_id":1,"store_id":1}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	group := 2
	customer, err := svc.UpdateCustomer(context.Background(), CustomerUpdateInput{CustomerID: 9, GroupID: &group})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := body["customer"].(map[string]any)
	if payload["group_id"] != float64(2) {
		t.Fatalf("body = %+v", payload)
	}
	if _, ok := payload["email"]; ok {
		t.Fatal("unset email must be omitted")
	}
	if customer.GroupID != 2 {
		t.Fatalf("customer = %+v", customer)
	}
}

func TestCustomerWriteValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.CreateCustomer(ctx, CustomerCreateInput{Email: "bad", Firstname: "A", Lastname: "B", WebsiteID: 1}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("email err = %v", err)
	}
	if _, err := svc.CreateCustomer(ctx, CustomerCreateInput{Email: "a@example.com", Firstname: "", Lastname: "B", WebsiteID: 1}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("name err = %v", err)
	}
	if _, err := svc.CreateCustomer(ctx, CustomerCreateInput{Email: "a@example.com", Firstname: "A", Lastname: "B", WebsiteID: 0}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("website err = %v", err)
	}
	if _, err := svc.UpdateCustomer(ctx, CustomerUpdateInput{CustomerID: 9}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty update err = %v", err)
	}
	bad := -1
	if _, err := svc.UpdateCustomer(ctx, CustomerUpdateInput{CustomerID: 9, GroupID: &bad}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("group err = %v", err)
	}
}

func TestCreateCustomerAddress(t *testing.T) {
	var method, path string
	var body customerAddressBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":5,"country_id":"GB","region":"London","default_billing":true,"default_shipping":false,
			"firstname":"Ada","street":["Secret Road 1"],"city":"London","postcode":"N1 9GU","telephone":"123"}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	address, err := svc.CreateCustomerAddress(context.Background(), 7, CustomerAddressInput{
		Firstname: "Ada", Lastname: "Lovelace", Street: []string{"1 Analytical Engine Way"},
		City: "London", Postcode: "N1 9GU", CountryID: "GB", Telephone: "0123456789", DefaultBilling: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/customers/7/addresses" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body.Address.CustomerID != 7 || body.Address.City != "London" || !body.Address.DefaultBilling {
		t.Fatalf("body = %+v", body.Address)
	}
	raw, _ := json.Marshal(address)
	for _, forbidden := range []string{"Secret Road", "Ada", "N1 9GU"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("projection leaks %q: %s", forbidden, raw)
		}
	}
	if address.ID != 5 || address.CountryID != "GB" || !address.DefaultBilling {
		t.Fatalf("address = %+v", address)
	}
}

func TestDeleteCustomerAddress(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.DeleteCustomerAddress(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/rest/default/V1/addresses/5" {
		t.Fatalf("request = %s %s", method, path)
	}
	if err := svc.DeleteCustomerAddress(context.Background(), 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}

func TestCustomerAddressWriteValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	valid := CustomerAddressInput{
		Firstname: "Ada", Lastname: "Lovelace", Street: []string{"1 Way"},
		City: "London", Postcode: "N1", CountryID: "GB", Telephone: "123",
	}
	cases := []CustomerAddressInput{
		{Firstname: "Ada", Lastname: "Lovelace", Street: []string{"1 Way"}, City: "London", Postcode: "N1", CountryID: "gb", Telephone: "123"},
		{Firstname: "", Lastname: "Lovelace", Street: []string{"1 Way"}, City: "London", Postcode: "N1", CountryID: "GB", Telephone: "123"},
		{Firstname: "Ada", Lastname: "Lovelace", Street: nil, City: "London", Postcode: "N1", CountryID: "GB", Telephone: "123"},
	}
	for i, in := range cases {
		if _, err := svc.CreateCustomerAddress(ctx, 7, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
	if _, err := svc.CreateCustomerAddress(ctx, 0, valid); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("customer id err = %v", err)
	}
}

func TestUpdateCustomerAddressPartial(t *testing.T) {
	var method, path string
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":5,"country_id":"GB","default_billing":false,"default_shipping":true}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	shipping := true
	address, err := svc.UpdateCustomerAddress(context.Background(), 7, 5, CustomerAddressUpdateInput{DefaultShipping: &shipping})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/rest/default/V1/customers/7/addresses/5" {
		t.Fatalf("request = %s %s", method, path)
	}
	payload, _ := body["address"].(map[string]any)
	if payload["default_shipping"] != true {
		t.Fatalf("body = %+v", payload)
	}
	if _, ok := payload["city"]; ok {
		t.Fatal("unset city must be omitted")
	}
	if !address.DefaultShipping || address.ID != 5 {
		t.Fatalf("address = %+v", address)
	}
}

func TestUpdateCustomerAddressValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.UpdateCustomerAddress(ctx, 0, 5, CustomerAddressUpdateInput{}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("ids err = %v", err)
	}
	if _, err := svc.UpdateCustomerAddress(ctx, 7, 5, CustomerAddressUpdateInput{}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty err = %v", err)
	}
	badCountry := "gb"
	if _, err := svc.UpdateCustomerAddress(ctx, 7, 5, CustomerAddressUpdateInput{CountryID: &badCountry}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("country err = %v", err)
	}
	emptyStreet := []string{}
	if _, err := svc.UpdateCustomerAddress(ctx, 7, 5, CustomerAddressUpdateInput{Street: &emptyStreet}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("street err = %v", err)
	}
}

// A store that exposes neither the dedicated address route nor a readable
// customer still gets the actionable capability error, not a bare not_found.
func TestCreateCustomerAddressUnsupportedRoute(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	_, err := svc.CreateCustomerAddress(context.Background(), 7, CustomerAddressInput{
		Firstname: "Ada", Lastname: "Lovelace", Street: []string{"1 Way"},
		City: "London", Postcode: "N1", CountryID: "GB", Telephone: "123",
	})
	if errs.CodeOf(err) != errs.CodeUnsupportedCapability {
		t.Fatalf("err = %v, want unsupported_capability", err)
	}
}
