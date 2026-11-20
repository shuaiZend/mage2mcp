// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultCustomerLimit = 20
	maxCustomerLimit     = 100
	maxEmailBytes        = 254
)

// Customer is the PII-safe customer projection this slice exposes.
//
// Email, name, and addresses are deliberately absent: callers can locate a
// customer, but the gateway never echoes personal data back through a tool.
type Customer struct {
	ID        int    `json:"id" jsonschema:"Magento entity id."`
	GroupID   int    `json:"group_id" jsonschema:"Customer group id, which decides pricing."`
	WebsiteID int    `json:"website_id" jsonschema:"Website the entity belongs to."`
	StoreID   int    `json:"store_id" jsonschema:"Store view the entity belongs to."`
	CreatedIn string `json:"created_in,omitempty" jsonschema:"Store view the account was created in."`
	CreatedAt string `json:"created_at,omitempty" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	UpdatedAt string `json:"updated_at,omitempty" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
}

// GetCustomer returns one customer by id with a PII-safe projection.
func (s *Service) GetCustomer(ctx context.Context, customerID int) (*Customer, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento customer service is not configured")
	}
	if customerID <= 0 {
		return nil, invalidf("customer_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/customers/"+strconv.Itoa(customerID), nil, nil)
	if err != nil {
		return nil, err
	}
	var payload customerWire
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable customer payload").WithCause(err)
	}
	return payload.projection(), nil
}

// SearchCustomers finds customers by exact email.
//
// Email is the only supported filter: it is the identifier operators have, and
// the response never contains it. Search returns a bounded page.
func (s *Service) SearchCustomers(ctx context.Context, email string, limit, offset int) ([]Customer, int, error) {
	if s == nil || s.client == nil {
		return nil, 0, errs.New(errs.CodeInternal, "Magento customer service is not configured")
	}
	if err := validateEmail(email); err != nil {
		return nil, 0, err
	}
	if limit == 0 {
		limit = defaultCustomerLimit
	}
	if limit < 1 || limit > maxCustomerLimit {
		return nil, 0, invalidf("limit must be between 1 and %d", maxCustomerLimit)
	}
	if offset < 0 || offset%limit != 0 {
		return nil, 0, invalidf("cursor offset must be a non-negative multiple of limit")
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(limit))
	query.Set("searchCriteria[currentPage]", strconv.Itoa(offset/limit+1))
	query.Set("searchCriteria[filter_groups][0][filters][0][field]", "email")
	query.Set("searchCriteria[filter_groups][0][filters][0][value]", email)
	query.Set("searchCriteria[filter_groups][0][filters][0][condition_type]", "eq")
	// total_count is part of the projection: without it Magento omits the field
	// and the reported total is always 0.
	query.Set("fields", "items[id,group_id,website_id,store_id,created_in,created_at,updated_at],total_count")
	raw, err := s.client.do(ctx, http.MethodGet, "/customers/search", query, nil)
	if err != nil {
		return nil, 0, err
	}
	var payload struct {
		Items      []customerWire `json:"items" jsonschema:"Entries returned by this call."`
		TotalCount int            `json:"total_count"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable customer search payload").WithCause(err)
	}
	customers := make([]Customer, 0, len(payload.Items))
	for _, item := range payload.Items {
		customers = append(customers, *item.projection())
	}
	return customers, payload.TotalCount, nil
}

// customerWire decodes only the fields needed for the projection; personal data
// in the upstream payload is ignored rather than stored.
type customerWire struct {
	ID        int    `json:"id" jsonschema:"Magento entity id."`
	GroupID   int    `json:"group_id" jsonschema:"Customer group id, which decides pricing."`
	WebsiteID int    `json:"website_id" jsonschema:"Website the entity belongs to."`
	StoreID   int    `json:"store_id" jsonschema:"Store view the entity belongs to."`
	CreatedIn string `json:"created_in" jsonschema:"Store view the account was created in."`
	CreatedAt string `json:"created_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	UpdatedAt string `json:"updated_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
}

func (w customerWire) projection() *Customer {
	return &Customer{
		ID: w.ID, GroupID: w.GroupID, WebsiteID: w.WebsiteID, StoreID: w.StoreID,
		CreatedIn: w.CreatedIn, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

func validateEmail(email string) error {
	if email == "" || len(email) > maxEmailBytes {
		return invalidf("email is required and must be at most %d bytes", maxEmailBytes)
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return invalidf("email is not a valid address")
	}
	return nil
}

// CustomerAddress is a PII-safe address projection: it keeps geography and
// defaults but omits name, street, city, postcode, and telephone.
type CustomerAddress struct {
	ID              int    `json:"id" jsonschema:"Magento entity id."`
	CountryID       string `json:"country_id" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
	Region          string `json:"region,omitempty" jsonschema:"Region name."`
	RegionID        int    `json:"region_id,omitempty" jsonschema:"Numeric region id; Magento requires it for countries with a region directory."`
	DefaultBilling  bool   `json:"default_billing" jsonschema:"True when this is the default billing address."`
	DefaultShipping bool   `json:"default_shipping" jsonschema:"True when this is the default shipping address."`
}

// CustomerAddresses returns the PII-safe addresses of one customer.
func (s *Service) CustomerAddresses(ctx context.Context, customerID int) ([]CustomerAddress, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento customer service is not configured")
	}
	if customerID <= 0 {
		return nil, invalidf("customer_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/customers/"+strconv.Itoa(customerID), nil, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Addresses []struct {
			ID              int         `json:"id" jsonschema:"Magento entity id."`
			CountryID       string      `json:"country_id" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
			Region          regionValue `json:"region" jsonschema:"Region name."`
			RegionID        int         `json:"region_id" jsonschema:"Numeric region id; Magento requires it for countries with a region directory."`
			DefaultBilling  bool        `json:"default_billing" jsonschema:"True when this is the default billing address."`
			DefaultShipping bool        `json:"default_shipping" jsonschema:"True when this is the default shipping address."`
		} `json:"addresses" jsonschema:"Customer addresses."`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable customer payload").WithCause(err)
	}
	addresses := make([]CustomerAddress, 0, len(payload.Addresses))
	for _, address := range payload.Addresses {
		regionID := address.RegionID
		if regionID == 0 {
			regionID = address.Region.ID
		}
		addresses = append(addresses, CustomerAddress{
			ID: address.ID, CountryID: address.CountryID, Region: address.Region.Name, RegionID: regionID,
			DefaultBilling: address.DefaultBilling, DefaultShipping: address.DefaultShipping,
		})
	}
	return addresses, nil
}

// CustomerCreateInput is one customer creation request. PII is accepted here
// because creating a customer requires it; the response is the PII-safe
// projection and never echoes it back.
type CustomerCreateInput struct {
	Email     string
	Firstname string
	Lastname  string
	WebsiteID int
	StoreID   int
	GroupID   int
}

// CustomerUpdateInput is a partial customer update.
type CustomerUpdateInput struct {
	CustomerID int
	Email      *string
	Firstname  *string
	Lastname   *string
	GroupID    *int
}

// CreateCustomer creates a customer with POST /customers.
func (s *Service) CreateCustomer(ctx context.Context, in CustomerCreateInput) (*Customer, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento customer service is not configured")
	}
	if err := validateEmail(in.Email); err != nil {
		return nil, err
	}
	if err := validateCustomerName("firstname", in.Firstname); err != nil {
		return nil, err
	}
	if err := validateCustomerName("lastname", in.Lastname); err != nil {
		return nil, err
	}
	if in.WebsiteID <= 0 {
		return nil, invalidf("website_id must be a positive integer")
	}
	if in.StoreID < 0 || in.GroupID < 0 {
		return nil, invalidf("store_id and group_id must not be negative")
	}
	body := customerWriteBody{Customer: customerWritePayload(in)}
	raw, err := s.client.do(ctx, http.MethodPost, "/customers", nil, body)
	if err != nil {
		return nil, err
	}
	return decodeCustomerProjection(raw)
}

// UpdateCustomer changes a customer with PUT /customers/{id}.
func (s *Service) UpdateCustomer(ctx context.Context, in CustomerUpdateInput) (*Customer, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento customer service is not configured")
	}
	if in.CustomerID <= 0 {
		return nil, invalidf("customer_id must be a positive integer")
	}
	if in.Email == nil && in.Firstname == nil && in.Lastname == nil && in.GroupID == nil {
		return nil, invalidf("at least one field must be provided to update")
	}
	payload := customerWritePayload{}
	if in.Email != nil {
		if err := validateEmail(*in.Email); err != nil {
			return nil, err
		}
		payload.Email = *in.Email
	}
	if in.Firstname != nil {
		if err := validateCustomerName("firstname", *in.Firstname); err != nil {
			return nil, err
		}
		payload.Firstname = *in.Firstname
	}
	if in.Lastname != nil {
		if err := validateCustomerName("lastname", *in.Lastname); err != nil {
			return nil, err
		}
		payload.Lastname = *in.Lastname
	}
	if in.GroupID != nil {
		if *in.GroupID < 0 {
			return nil, invalidf("group_id must not be negative")
		}
		payload.GroupID = *in.GroupID
	}
	body := customerWriteBody{Customer: payload}
	raw, err := s.client.do(ctx, http.MethodPut, "/customers/"+strconv.Itoa(in.CustomerID), nil, body)
	if err != nil {
		return nil, err
	}
	return decodeCustomerProjection(raw)
}

type customerWriteBody struct {
	Customer customerWritePayload `json:"customer" jsonschema:"The customer that was created or read."`
}

type customerWritePayload struct {
	Email     string `json:"email,omitempty" jsonschema:"Customer email."`
	Firstname string `json:"firstname,omitempty" jsonschema:"Given name."`
	Lastname  string `json:"lastname,omitempty" jsonschema:"Family name."`
	WebsiteID int    `json:"website_id,omitempty" jsonschema:"Website the entity belongs to."`
	StoreID   int    `json:"store_id,omitempty" jsonschema:"Store view the entity belongs to."`
	GroupID   int    `json:"group_id,omitempty" jsonschema:"Customer group id, which decides pricing."`
}

func decodeCustomerProjection(raw []byte) (*Customer, error) {
	var payload customerWire
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable customer payload").WithCause(err)
	}
	return payload.projection(), nil
}

func validateCustomerName(field, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > 255 {
		return invalidf("%s is required and must be at most 255 bytes", field)
	}
	if strings.ContainsFunc(trimmed, unicode.IsControl) {
		return invalidf("%s must not contain control characters", field)
	}
	return nil
}

// CustomerAddressInput is one address to create for a customer. PII is accepted
// because an address requires it; the response is the PII-safe projection.
type CustomerAddressInput struct {
	Firstname string
	Lastname  string
	Street    []string
	City      string
	Postcode  string
	CountryID string
	Telephone string
	Region    string
	// RegionID is the numeric region id Magento requires for countries with a
	// region directory.
	RegionID        int
	DefaultBilling  bool
	DefaultShipping bool
}

// CreateCustomerAddress adds an address with
// POST /customers/{customerId}/addresses.
func (s *Service) CreateCustomerAddress(ctx context.Context, customerID int, in CustomerAddressInput) (*CustomerAddress, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento customer service is not configured")
	}
	if customerID <= 0 {
		return nil, invalidf("customer_id must be a positive integer")
	}
	address := OrderAddressInput{
		Firstname: in.Firstname, Lastname: in.Lastname, Street: in.Street,
		City: in.City, Postcode: in.Postcode, CountryID: in.CountryID,
		Telephone: in.Telephone, Region: in.Region,
	}
	if err := validateOrderAddress("address", address); err != nil {
		return nil, err
	}
	body := customerAddressBody{Address: customerAddressPayload{
		CustomerID: customerID, Firstname: in.Firstname, Lastname: in.Lastname,
		Street: in.Street, City: in.City, Postcode: in.Postcode, CountryID: in.CountryID,
		Telephone: in.Telephone, Region: in.Region, RegionID: in.RegionID,
		DefaultBilling: in.DefaultBilling, DefaultShipping: in.DefaultShipping,
	}}
	raw, err := s.client.do(ctx, http.MethodPost, "/customers/"+strconv.Itoa(customerID)+"/addresses", nil, body)
	if err == nil {
		return decodeCustomerAddress(raw)
	}
	if errs.CodeOf(err) != errs.CodeNotFound {
		return nil, err
	}
	// Not every build exposes a dedicated address route. The supported fallback is
	// to PUT the customer with the new address appended, which is what the old
	// error message told the operator to do by hand.
	created, fallbackErr := s.createAddressViaCustomerUpdate(ctx, customerID, body.Address)
	if fallbackErr != nil && errs.CodeOf(fallbackErr) == errs.CodeNotFound {
		// Both the dedicated route and the customer read are missing: report the
		// actionable capability error rather than a bare not_found.
		return nil, addressRouteError(fallbackErr, "creation")
	}
	return created, fallbackErr
}

// createAddressViaCustomerUpdate adds an address by updating the customer, for
// instances that do not expose a customer-address create route.
//
// The customer is round-tripped as a generic object so every field the store
// returned is preserved; only addresses is appended to.
func (s *Service) createAddressViaCustomerUpdate(ctx context.Context, customerID int, address customerAddressPayload) (*CustomerAddress, error) {
	path := "/customers/" + strconv.Itoa(customerID)
	raw, err := s.client.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	var current map[string]any
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable customer payload").WithCause(err)
	}
	existing, _ := current["addresses"].([]any)
	// The update body must carry the identity fields the service contract requires.
	for key, value := range map[string]any{
		"id": customerID, "website_id": current["website_id"],
		"email": current["email"], "firstname": current["firstname"], "lastname": current["lastname"],
	} {
		if _, ok := current[key]; !ok && value != nil {
			current[key] = value
		}
	}
	current["id"] = customerID
	current["addresses"] = append(existing, map[string]any{
		"firstname": address.Firstname, "lastname": address.Lastname,
		"street": address.Street, "city": address.City, "postcode": address.Postcode,
		"country_id": address.CountryID, "telephone": address.Telephone,
		"region": address.Region, "default_billing": address.DefaultBilling,
		"default_shipping": address.DefaultShipping,
	})
	if _, err := s.client.do(ctx, http.MethodPut, path, nil, map[string]any{"customer": current}); err != nil {
		return nil, err
	}
	// Read back and return the address the store assigned, identified by the
	// fields we sent plus the largest id among matches.
	raw, err = s.client.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	var updated struct {
		Addresses []map[string]any `json:"addresses" jsonschema:"Customer addresses."`
	}
	if err := json.Unmarshal(raw, &updated); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable customer payload").WithCause(err)
	}
	bestID := 0
	for _, candidate := range updated.Addresses {
		if fmt.Sprint(candidate["city"]) != address.City {
			continue
		}
		if id, ok := candidate["id"].(float64); ok && int(id) > bestID {
			bestID = int(id)
		}
	}
	if bestID == 0 {
		return nil, errs.New(errs.CodeUpstreamUnavailable,
			"Magento accepted the address but the customer does not list it; verify before retrying").
			WithDetail("customer_id", strconv.Itoa(customerID))
	}
	return &CustomerAddress{
		ID: bestID, CountryID: address.CountryID, Region: address.Region,
		DefaultBilling: address.DefaultBilling, DefaultShipping: address.DefaultShipping,
	}, nil
}

// DeleteCustomerAddress removes an address with DELETE /addresses/{id}.
func (s *Service) DeleteCustomerAddress(ctx context.Context, addressID int) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento customer service is not configured")
	}
	if addressID <= 0 {
		return invalidf("address_id must be a positive integer")
	}
	_, err := s.client.do(ctx, http.MethodDelete, "/addresses/"+strconv.Itoa(addressID), nil, nil)
	if err != nil {
		return addressRouteError(err, "deletion")
	}
	return nil
}

type customerAddressBody struct {
	Address customerAddressPayload `json:"address" jsonschema:"The customer address that was written."`
}

type customerAddressPayload struct {
	CustomerID      int      `json:"customer_id,omitempty"`
	Firstname       string   `json:"firstname,omitempty" jsonschema:"Given name."`
	Lastname        string   `json:"lastname,omitempty" jsonschema:"Family name."`
	Street          []string `json:"street,omitempty" jsonschema:"Street lines, most specific first."`
	City            string   `json:"city,omitempty" jsonschema:"City or locality."`
	Postcode        string   `json:"postcode,omitempty" jsonschema:"Postal code as the destination country writes it."`
	CountryID       string   `json:"country_id,omitempty" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
	Telephone       string   `json:"telephone,omitempty" jsonschema:"Contact phone number."`
	Region          string   `json:"region,omitempty" jsonschema:"Region name."`
	RegionID        int      `json:"region_id,omitempty" jsonschema:"Numeric region id; Magento requires it for countries with a region directory."`
	DefaultBilling  bool     `json:"default_billing,omitempty" jsonschema:"True when this is the default billing address."`
	DefaultShipping bool     `json:"default_shipping,omitempty" jsonschema:"True when this is the default shipping address."`
}

func decodeCustomerAddress(raw []byte) (*CustomerAddress, error) {
	var payload struct {
		ID              int    `json:"id" jsonschema:"Magento entity id."`
		CountryID       string `json:"country_id" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
		Region          string `json:"region" jsonschema:"Region name."`
		RegionID        int    `json:"region_id" jsonschema:"Numeric region id; Magento requires it for countries with a region directory."`
		DefaultBilling  bool   `json:"default_billing" jsonschema:"True when this is the default billing address."`
		DefaultShipping bool   `json:"default_shipping" jsonschema:"True when this is the default shipping address."`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable address payload").WithCause(err)
	}
	return &CustomerAddress{
		ID: payload.ID, CountryID: payload.CountryID, Region: payload.Region,
		RegionID: payload.RegionID, DefaultBilling: payload.DefaultBilling, DefaultShipping: payload.DefaultShipping,
	}, nil
}

// CustomerAddressUpdateInput is a partial address update; nil fields are omitted.
type CustomerAddressUpdateInput struct {
	Firstname       *string
	Lastname        *string
	Street          *[]string
	City            *string
	Postcode        *string
	CountryID       *string
	Telephone       *string
	Region          *string
	RegionID        *int
	DefaultBilling  *bool
	DefaultShipping *bool
}

// UpdateCustomerAddress changes an address with
// PUT /customers/{customerId}/addresses/{addressId}.
func (s *Service) UpdateCustomerAddress(ctx context.Context, customerID, addressID int, in CustomerAddressUpdateInput) (*CustomerAddress, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento customer service is not configured")
	}
	if customerID <= 0 || addressID <= 0 {
		return nil, invalidf("customer_id and address_id must be positive integers")
	}
	if in.Firstname == nil && in.Lastname == nil && in.Street == nil && in.City == nil &&
		in.Postcode == nil && in.CountryID == nil && in.Telephone == nil && in.Region == nil &&
		in.DefaultBilling == nil && in.DefaultShipping == nil {
		return nil, invalidf("at least one field must be provided to update")
	}
	payload := customerAddressPayload{CustomerID: customerID}
	if in.Firstname != nil {
		if err := validateCustomerName("firstname", *in.Firstname); err != nil {
			return nil, err
		}
		payload.Firstname = *in.Firstname
	}
	if in.Lastname != nil {
		if err := validateCustomerName("lastname", *in.Lastname); err != nil {
			return nil, err
		}
		payload.Lastname = *in.Lastname
	}
	if in.Street != nil {
		if len(*in.Street) == 0 || len(*in.Street) > maxAddressStreet {
			return nil, invalidf("street must have 1-%d lines", maxAddressStreet)
		}
		for _, line := range *in.Street {
			if trimmed := strings.TrimSpace(line); trimmed == "" || len(line) > maxAddressBytes {
				return nil, invalidf("street lines must be non-empty and at most %d bytes", maxAddressBytes)
			}
		}
		payload.Street = *in.Street
	}
	if in.City != nil {
		if err := validateCustomerName("city", *in.City); err != nil {
			return nil, err
		}
		payload.City = *in.City
	}
	if in.Postcode != nil {
		if err := validateCustomerName("postcode", *in.Postcode); err != nil {
			return nil, err
		}
		payload.Postcode = *in.Postcode
	}
	if in.CountryID != nil {
		if len(*in.CountryID) != 2 || strings.ToUpper(*in.CountryID) != *in.CountryID {
			return nil, invalidf("country_id must be a two-letter uppercase ISO country code")
		}
		payload.CountryID = *in.CountryID
	}
	if in.Telephone != nil {
		if err := validateCustomerName("telephone", *in.Telephone); err != nil {
			return nil, err
		}
		payload.Telephone = *in.Telephone
	}
	if in.Region != nil {
		if len(*in.Region) > maxAddressBytes {
			return nil, invalidf("region must be at most %d bytes", maxAddressBytes)
		}
		payload.Region = *in.Region
	}
	if in.DefaultBilling != nil {
		payload.DefaultBilling = *in.DefaultBilling
	}
	if in.DefaultShipping != nil {
		payload.DefaultShipping = *in.DefaultShipping
	}
	body := customerAddressBody{Address: payload}
	raw, err := s.client.do(ctx, http.MethodPut,
		"/customers/"+strconv.Itoa(customerID)+"/addresses/"+strconv.Itoa(addressID), nil, body)
	if err != nil {
		return nil, addressRouteError(err, "update")
	}
	return decodeCustomerAddress(raw)
}

// addressRouteError turns a missing customer-address route into an actionable
// capability error instead of a generic 404.
func addressRouteError(err error, action string) error {
	if errs.CodeOf(err) != errs.CodeNotFound {
		return err
	}
	return errs.New(errs.CodeUnsupportedCapability,
		"this Magento instance does not expose customer address "+action+
			"; update the customer with an addresses array or use the admin UI").
		WithDetail("upstream_status", "404")
}

// regionValue decodes Magento's `region` field, which is a string on some responses
// and an object on others.
//
// The customer address read returns {"region_code","region","region_id"} while the
// order address read returns a plain string. Decoding both into a string made the
// customer address list fail with "unreadable customer payload" for any customer that
// has an address — the array element was rejected, not the envelope.
type regionValue struct {
	Name string
	ID   int
}

// UnmarshalJSON accepts either shape.
func (r *regionValue) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '{' {
		var object struct {
			Region   string `json:"region"`
			RegionID int    `json:"region_id"`
		}
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return err
		}
		r.Name, r.ID = object.Region, object.RegionID
		return nil
	}
	return json.Unmarshal(trimmed, &r.Name)
}
