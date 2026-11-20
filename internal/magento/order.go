// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultOrderLimit = 50
	maxOrderLimit     = 200
	// maxSummaryOrders bounds a sales summary so one call cannot scan an
	// unbounded order history.
	maxSummaryOrders     = 1000
	maxSummaryWindowDays = 366
)

var (
	orderStatusRE    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	incrementIDRE    = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)
	orderSearchField = "items[entity_id,increment_id,status,state,store_id,customer_id," +
		"created_at,updated_at,grand_total,subtotal,discount_amount,shipping_amount," +
		"tax_amount,total_qty_ordered,order_currency_code]," +
		// total_count must be projected explicitly: `fields` is a whitelist, so
		// omitting it makes Magento omit the field and every total reads as 0.
		"total_count"
)

// OrderItem is one line of an order. It carries no customer data.
type OrderItem struct {
	ItemID      int     `json:"item_id"`
	SKU         string  `json:"sku"`
	Name        string  `json:"name"`
	ProductType string  `json:"product_type"`
	QtyOrdered  float64 `json:"qty_ordered"`
	Price       float64 `json:"price"`
	RowTotal    float64 `json:"row_total"`
}

// Order is the explicit order projection this slice exposes.
//
// Customer email, addresses, and payment details are intentionally absent:
// order analytics does not need them, and omitting them keeps the tool free of
// personal data. Items are populated by GetOrder, not by SearchOrders.
type Order struct {
	EntityID        int         `json:"entity_id"`
	IncrementID     string      `json:"increment_id"`
	Status          string      `json:"status"`
	State           string      `json:"state"`
	StoreID         int         `json:"store_id"`
	CustomerID      *int        `json:"customer_id,omitempty"`
	CreatedAt       string      `json:"created_at"`
	UpdatedAt       string      `json:"updated_at"`
	GrandTotal      float64     `json:"grand_total"`
	Subtotal        float64     `json:"subtotal"`
	DiscountAmount  float64     `json:"discount_amount"`
	ShippingAmount  float64     `json:"shipping_amount"`
	TaxAmount       float64     `json:"tax_amount"`
	TotalQtyOrdered float64     `json:"total_qty_ordered"`
	Currency        string      `json:"order_currency_code"`
	Items           []OrderItem `json:"items,omitempty"`
}

// OrderQuery selects orders; zero values mean no filter except Limit (50).
type OrderQuery struct {
	Status       string // order status, e.g. processing
	State        string // order state, e.g. complete
	IncrementID  string // exact increment id, e.g. 000000123
	CreatedFrom  string // RFC 3339 or YYYY-MM-DD, inclusive
	CreatedTo    string // RFC 3339 or YYYY-MM-DD, exclusive end of day when date-only
	StoreID      int    // store view id filter
	CustomerID   int    // customer id filter (0 means any)
	Limit        int    // page size 1-200; zero selects 50
	CursorOffset int    // zero-based row offset
}

// GetOrder returns one order by entity id, including its line items.
func (s *Service) GetOrder(ctx context.Context, orderID int) (*Order, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento order service is not configured")
	}
	if orderID <= 0 {
		return nil, invalidf("order_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/orders/"+strconv.Itoa(orderID), nil, nil)
	if err != nil {
		return nil, err
	}
	var order Order
	if err := json.Unmarshal(raw, &order); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable order payload").WithCause(err)
	}
	return &order, nil
}

// SearchOrders returns one page of orders and the total match count.
//
// Line items are not returned by search; use GetOrder for the detail.
func (s *Service) SearchOrders(ctx context.Context, q OrderQuery) ([]Order, int, error) {
	if s == nil || s.client == nil {
		return nil, 0, errs.New(errs.CodeInternal, "Magento order service is not configured")
	}
	query, err := orderSearchCriteria(q)
	if err != nil {
		return nil, 0, err
	}
	query.Set("fields", orderSearchField)
	raw, err := s.client.do(ctx, http.MethodGet, "/orders", query, nil)
	if err != nil {
		return nil, 0, err
	}
	var payload struct {
		Items      []Order `json:"items"`
		TotalCount int     `json:"total_count"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable order search payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []Order{}
	}
	return payload.Items, payload.TotalCount, nil
}

// SalesDay is one calendar day of aggregated sales.
type SalesDay struct {
	Date    string  `json:"date"`
	Orders  int     `json:"orders"`
	Revenue float64 `json:"revenue"`
	Items   float64 `json:"items"`
}

// SalesSummary aggregates orders per day over a bounded window.
//
// Completeness is explicit: TotalAvailable is the number of orders Magento
// reports for the window, ScannedOrders is how many were aggregated, and
// ExcludedOrders is the difference. Truncated is true only when at least one
// order was excluded.
type SalesSummary struct {
	Window         string     `json:"window"`
	Days           []SalesDay `json:"days"`
	TotalOrders    int        `json:"total_orders"`
	TotalRevenue   float64    `json:"total_revenue"`
	TotalItems     float64    `json:"total_items"`
	Currency       string     `json:"currency,omitempty"`
	MixedCurrency  bool       `json:"mixed_currency"`
	Truncated      bool       `json:"truncated"`
	TotalAvailable int        `json:"total_available"`
	ExcludedOrders int        `json:"excluded_orders"`
	ScannedOrders  int        `json:"scanned_orders"`
}

// SalesSummary aggregates order count, revenue, and item quantity per day.
//
// It scans at most maxSummaryOrders orders (newest first) and reports whether
// the window was truncated. Revenue is order grand_total, which includes tax
// and shipping; it is not comparable to GA4 purchase revenue without stating
// the difference.
func (s *Service) SalesSummary(ctx context.Context, from, to time.Time) (*SalesSummary, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento order service is not configured")
	}
	if !to.After(from) {
		return nil, invalidf("to must be after from")
	}
	if to.Sub(from) > maxSummaryWindowDays*24*time.Hour {
		return nil, invalidf("sales window must be at most %d days", maxSummaryWindowDays)
	}
	summary := &SalesSummary{
		Window: from.UTC().Format(time.RFC3339) + "/" + to.UTC().Format(time.RFC3339),
		Days:   []SalesDay{},
	}
	byDate := map[string]*SalesDay{}
	limit := maxOrderLimit
	totalAvailable := 0
	// scannedAll is set when a page came back short, which is the only proof that
	// the window holds no further orders. Reaching the cap without it means the
	// scan was truncated whether or not total_count was available.
	scannedAll := false
	for offset := 0; offset < maxSummaryOrders; offset += limit {
		query, err := orderSearchCriteria(OrderQuery{
			CreatedFrom:  from.UTC().Format(time.RFC3339),
			CreatedTo:    to.UTC().Format(time.RFC3339),
			Limit:        limit,
			CursorOffset: offset,
		})
		if err != nil {
			return nil, err
		}
		query.Set("fields", orderSearchField)
		raw, err := s.client.do(ctx, http.MethodGet, "/orders", query, nil)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Items      []Order `json:"items"`
			TotalCount int     `json:"total_count"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable order search payload").WithCause(err)
		}
		for _, order := range payload.Items {
			date := orderDate(order.CreatedAt)
			if date == "" {
				continue
			}
			day := byDate[date]
			if day == nil {
				day = &SalesDay{Date: date}
				byDate[date] = day
			}
			day.Orders++
			day.Revenue += order.GrandTotal
			day.Items += order.TotalQtyOrdered
			summary.TotalOrders++
			summary.TotalRevenue += order.GrandTotal
			summary.TotalItems += order.TotalQtyOrdered
			summary.ScannedOrders++
			if summary.Currency == "" {
				summary.Currency = order.Currency
			} else if order.Currency != "" && order.Currency != summary.Currency {
				summary.MixedCurrency = true
			}
		}
		if payload.TotalCount > 0 {
			totalAvailable = payload.TotalCount
		}
		if len(payload.Items) < limit {
			scannedAll = true
			break
		}
	}
	// Truncation is a property of the finished scan, not of the first page: the
	// flag used to latch as soon as total_count exceeded the first page size, so
	// a complete 924-order scan reported truncated=true.
	summary.TotalAvailable = totalAvailable
	if summary.TotalAvailable < summary.ScannedOrders {
		summary.TotalAvailable = summary.ScannedOrders
	}
	summary.ExcludedOrders = summary.TotalAvailable - summary.ScannedOrders
	// Truncation is a property of the finished scan. Two independent facts can
	// establish it: a known total larger than what we scanned, or a scan that
	// stopped at the cap without ever seeing a short page. Relying on the total
	// alone was wrong — the total was omitted by the fields projection, so a
	// capped scan reported truncated=false and claimed completeness.
	summary.Truncated = summary.ExcludedOrders > 0 || !scannedAll
	for _, day := range byDate {
		summary.Days = append(summary.Days, *day)
	}
	sortSalesDays(summary.Days)
	return summary, nil
}

func orderSearchCriteria(q OrderQuery) (url.Values, error) {
	limit := q.Limit
	if limit == 0 {
		limit = defaultOrderLimit
	}
	if limit < 1 || limit > maxOrderLimit {
		return nil, invalidf("limit must be between 1 and %d", maxOrderLimit)
	}
	if q.CursorOffset < 0 || q.CursorOffset%limit != 0 {
		return nil, invalidf("cursor offset must be a non-negative multiple of limit")
	}
	if q.Status != "" && !orderStatusRE.MatchString(q.Status) {
		return nil, invalidf("status is not a valid Magento order status")
	}
	if q.State != "" && !orderStatusRE.MatchString(q.State) {
		return nil, invalidf("state is not a valid Magento order state")
	}
	if q.IncrementID != "" && !incrementIDRE.MatchString(q.IncrementID) {
		return nil, invalidf("increment_id must be 1-32 characters from [A-Za-z0-9-]")
	}
	if q.StoreID < 0 {
		return nil, invalidf("store_id must not be negative")
	}
	if q.CustomerID < 0 {
		return nil, invalidf("customer_id must not be negative")
	}
	createdFrom, err := parseOrderTime(q.CreatedFrom, "created_from")
	if err != nil {
		return nil, err
	}
	createdTo, err := parseOrderTime(q.CreatedTo, "created_to")
	if err != nil {
		return nil, err
	}
	if createdFrom != "" && createdTo != "" && createdTo < createdFrom {
		return nil, invalidf("created_to must not be before created_from")
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(limit))
	query.Set("searchCriteria[currentPage]", strconv.Itoa(q.CursorOffset/limit+1))
	group := 0
	addFilter := func(field, value, condition string) {
		prefix := fmt.Sprintf("searchCriteria[filter_groups][%d][filters][0]", group)
		query.Set(prefix+"[field]", field)
		query.Set(prefix+"[value]", value)
		query.Set(prefix+"[condition_type]", condition)
		group++
	}
	if q.IncrementID != "" {
		addFilter("increment_id", q.IncrementID, "eq")
	}
	if q.Status != "" {
		addFilter("status", q.Status, "eq")
	}
	if q.State != "" {
		addFilter("state", q.State, "eq")
	}
	if createdFrom != "" {
		addFilter("created_at", createdFrom, "gteq")
	}
	if createdTo != "" {
		addFilter("created_at", createdTo, "lt")
	}
	if q.StoreID > 0 {
		addFilter("store_id", strconv.Itoa(q.StoreID), "eq")
	}
	if q.CustomerID > 0 {
		addFilter("customer_id", strconv.Itoa(q.CustomerID), "eq")
	}
	return query, nil
}

// parseOrderTime accepts RFC 3339 or a date and returns Magento's UTC format.
func parseOrderTime(value, field string) (string, error) {
	if value == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05"), nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05"), nil
	}
	return "", invalidf("%s must be an RFC 3339 timestamp or YYYY-MM-DD date", field)
}

func orderDate(createdAt string) string {
	if len(createdAt) >= 10 && createdAt[4] == '-' && createdAt[7] == '-' {
		return createdAt[:10]
	}
	return ""
}

func sortSalesDays(days []SalesDay) {
	for i := 1; i < len(days); i++ {
		for j := i; j > 0 && strings.Compare(days[j].Date, days[j-1].Date) < 0; j-- {
			days[j], days[j-1] = days[j-1], days[j]
		}
	}
}
