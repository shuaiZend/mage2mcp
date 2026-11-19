// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
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
	ID        int    `json:"id"`
	GroupID   int    `json:"group_id"`
	WebsiteID int    `json:"website_id"`
	StoreID   int    `json:"store_id"`
	CreatedIn string `json:"created_in,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
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
	query.Set("fields", "items[id,group_id,website_id,store_id,created_in,created_at,updated_at]")
	raw, err := s.client.do(ctx, http.MethodGet, "/customers/search", query, nil)
	if err != nil {
		return nil, 0, err
	}
	var payload struct {
		Items      []customerWire `json:"items"`
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
	ID        int    `json:"id"`
	GroupID   int    `json:"group_id"`
	WebsiteID int    `json:"website_id"`
	StoreID   int    `json:"store_id"`
	CreatedIn string `json:"created_in"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
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
	ID              int    `json:"id"`
	CountryID       string `json:"country_id"`
	Region          string `json:"region,omitempty"`
	RegionID        int    `json:"region_id,omitempty"`
	DefaultBilling  bool   `json:"default_billing"`
	DefaultShipping bool   `json:"default_shipping"`
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
			ID              int    `json:"id"`
			CountryID       string `json:"country_id"`
			Region          string `json:"region"`
			RegionID        int    `json:"region_id"`
			DefaultBilling  bool   `json:"default_billing"`
			DefaultShipping bool   `json:"default_shipping"`
		} `json:"addresses"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable customer payload").WithCause(err)
	}
	addresses := make([]CustomerAddress, 0, len(payload.Addresses))
	for _, address := range payload.Addresses {
		addresses = append(addresses, CustomerAddress{
			ID: address.ID, CountryID: address.CountryID, Region: address.Region, RegionID: address.RegionID,
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
	body := customerWriteBody{Customer: customerWritePayload{
		Email: in.Email, Firstname: in.Firstname, Lastname: in.Lastname,
		WebsiteID: in.WebsiteID, StoreID: in.StoreID, GroupID: in.GroupID,
	}}
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
	Customer customerWritePayload `json:"customer"`
}

type customerWritePayload struct {
	Email     string `json:"email,omitempty"`
	Firstname string `json:"firstname,omitempty"`
	Lastname  string `json:"lastname,omitempty"`
	WebsiteID int    `json:"website_id,omitempty"`
	StoreID   int    `json:"store_id,omitempty"`
	GroupID   int    `json:"group_id,omitempty"`
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
