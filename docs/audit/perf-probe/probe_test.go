//go:build probe

// SPDX-License-Identifier: Apache-2.0

// Package perfprobe is the reproducible measurement harness behind
// docs/audit/performance-review.md.
//
// It is audit evidence, not production code: it drives the real
// internal/magento client against a fake Magento REST server and records
// outbound request counts, pagination behaviour, allocation, and connection
// churn. No live Magento instance is required.
//
// Run:
//
//	go test ./docs/audit/perf-probe/ -run TestPerf -v -count=1
//	go test ./docs/audit/perf-probe/ -bench . -benchmem -run '^$' -count=1
package perfprobe

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/magento"
)

// ---------------------------------------------------------------- fake server

type recorder struct {
	mu          sync.Mutex
	total       int
	byKey       map[string]int
	missingSKUs map[string]bool
}

func newRecorder() *recorder { return &recorder{byKey: map[string]int{}} }

func (r *recorder) hit(key string) {
	r.mu.Lock()
	r.total++
	r.byKey[key]++
	r.mu.Unlock()
}

func (r *recorder) reset() {
	r.mu.Lock()
	r.total = 0
	r.byKey = map[string]int{}
	r.mu.Unlock()
}

func (r *recorder) snapshot() (int, map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.byKey))
	for k, v := range r.byKey {
		out[k] = v
	}
	return r.total, out
}

type fake struct {
	rec     *recorder
	latency time.Duration
	pages   func(page, size int) []byte // order search payload for a page
	ship    []byte
	product []byte
	conns   int32
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.latency > 0 {
		time.Sleep(f.latency)
	}
	path := strings.TrimPrefix(r.URL.Path, "/rest/default/V1")
	page, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria[currentPage]"))
	if page == 0 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria[pageSize]"))
	if size == 0 {
		size = 20
	}
	switch {
	case path == "/orders" && r.Method == http.MethodGet:
		f.rec.hit("GET /orders")
		writeRaw(w, f.pages(page, size))
	case path == "/shipments":
		f.rec.hit("GET /shipments")
		writeRaw(w, f.ship)
	case path == "/products" && r.Method == http.MethodGet:
		f.rec.hit("GET /products")
		writeRaw(w, []byte(`{"items":[],"total_count":0}`))
	case path == "/products" && r.Method == http.MethodPost:
		f.rec.hit("POST /products")
		writeRaw(w, f.product)
	case strings.HasPrefix(path, "/products/") && strings.Contains(path, "/media"):
		f.rec.hit("media")
		writeRaw(w, []byte(`[]`))
	case strings.HasPrefix(path, "/products/") && strings.Contains(path, "/stockItems/"):
		f.rec.hit("PUT /products/{sku}/stockItems")
		writeRaw(w, []byte(`true`))
	case strings.HasPrefix(path, "/products/") && r.Method == http.MethodGet:
		sku := strings.TrimPrefix(path, "/products/")
		if f.rec.missing(sku) {
			f.rec.hit("GET /products/{sku} (404)")
			w.WriteHeader(http.StatusNotFound)
			writeRaw(w, []byte(`{"message":"The product that was requested doesn't exist."}`))
			return
		}
		f.rec.hit("GET /products/{sku}")
		writeRaw(w, productJSON(sku))
	case strings.HasPrefix(path, "/products/") && r.Method == http.MethodPut:
		f.rec.hit("PUT /products/{sku}")
		writeRaw(w, productJSON(strings.TrimPrefix(path, "/products/")))
	case strings.HasPrefix(path, "/stockItems/"):
		f.rec.hit("GET /stockItems/{sku}")
		writeRaw(w, []byte(`{"qty":7,"is_in_stock":true,"manage_stock":true}`))
	case path == "/inventory/source-items":
		f.rec.hit("POST /inventory/source-items")
		writeRaw(w, []byte(`[]`))
	case strings.HasPrefix(path, "/products/attributes/") && strings.HasSuffix(path, "/options"):
		f.rec.hit("POST /products/attributes/{code}/options")
		writeRaw(w, []byte(`"987"`))
	case strings.HasPrefix(path, "/configurable-products/") && strings.HasSuffix(path, "/child"):
		f.rec.hit("POST /configurable-products/{sku}/child")
		writeRaw(w, []byte(`true`))
	case path == "/store/storeConfigs":
		f.rec.hit("GET /store/storeConfigs")
		writeRaw(w, []byte(`[{"timezone":"UTC"}]`))
	default:
		f.rec.hit("UNEXPECTED " + r.Method + " " + path)
		w.WriteHeader(http.StatusNotFound)
		writeRaw(w, []byte(`{"message":"no route"}`))
	}
}

// missing reports whether the requested SKU should answer 404. It is used by the
// import probe to force the create branch.
func (r *recorder) missing(sku string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.missingSKUs[sku]
}

func writeRaw(w http.ResponseWriter, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func productJSON(sku string) []byte {
	return []byte(`{"id":1,"sku":"` + sku + `","name":"P ` + sku + `","type_id":"simple","price":9.5,` +
		`"status":1,"visibility":4,"attribute_set_id":4,"created_at":"2026-10-01 00:00:00"}`)
}

func startFake(t *testing.T, f *fake) *httptest.Server {
	t.Helper()
	if f.rec == nil {
		f.rec = newRecorder()
	}
	if f.ship == nil {
		f.ship = []byte(`{"items":[],"total_count":0}`)
	}
	if f.product == nil {
		f.product = productJSON("created")
	}
	if f.pages == nil {
		f.pages = func(int, int) []byte { return []byte(`{"items":[],"total_count":0}`) }
	}
	srv := httptest.NewUnstartedServer(f)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			atomic.AddInt32(&f.conns, 1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func newService(t *testing.T, baseURL string, httpClient *http.Client) *magento.Service {
	t.Helper()
	signer, err := magento.NewOAuth1Signer("key", "secret", "token", "token-secret")
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	client, err := magento.NewClient(magento.ClientConfig{
		BaseURL:    baseURL,
		StoreCode:  "default",
		Signer:     signer,
		HTTPClient: httpClient,
		Timeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	svc, err := magento.NewService(client)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	return svc
}

func mustService(t *testing.T, f *fake) *magento.Service {
	srv := startFake(t, f)
	return newService(t, srv.URL, nil)
}

func report(t *testing.T, label string, total int, byKey map[string]int) {
	t.Helper()
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	// stable order for reproducible logs
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%d", k, byKey[k])
	}
	t.Logf("PROBE %-38s total_requests=%d%s", label, total, b.String())
}

// ------------------------------------------------------------- order fixtures

const orderItemCount = 4

// orderPages returns a page producer over n synthetic orders, each carrying
// orderItemCount line items, in the wire shape Magento returns for /orders.
func orderPages(n int) func(page, size int) []byte {
	start := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	base := []byte(`{"items":[`)
	return func(page, size int) []byte {
		from := (page - 1) * size
		if from >= n {
			return []byte(`{"items":[],"total_count":` + strconv.Itoa(n) + `}`)
		}
		to := from + size
		if to > n {
			to = n
		}
		buf := make([]byte, 0, len(base)+4096)
		buf = append(buf, base...)
		for i := from; i < to; i++ {
			if i > from {
				buf = append(buf, ',')
			}
			created := start.Add(time.Duration(i) * 37 * time.Minute).Format("2006-01-02 15:04:05")
			buf = append(buf, fmt.Sprintf(`{"entity_id":%d,"created_at":"%s","status":"processing",`+
				`"grand_total":%d.50,"subtotal":%d.00,"discount_amount":0,"shipping_amount":5,`+
				`"tax_amount":1.2,"total_qty_ordered":%d,"order_currency_code":"USD",`+
				`"store_name":"Main Website\nMain Website Store\nDefault Store View",`+
				`"customer_id":%d,"customer_is_guest":0,"coupon_code":"BF2026",`+
				`"shipping_description":"Flat Rate - Fixed","payment":{"method":"checkmo"},`+
				`"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]},`+
				`"items":[`,
				i+1, created, 100+i%50, 95+i%50, orderItemCount, 1000+i%700)...)
			for j := 0; j < orderItemCount; j++ {
				if j > 0 {
					buf = append(buf, ',')
				}
				buf = append(buf, fmt.Sprintf(`{"sku":"SKU-%04d","name":"Item %d","product_type":"simple",`+
					`"qty_ordered":1,"row_total":%d.5,"qty_refunded":0,"amount_refunded":0}`,
					(i*orderItemCount+j)%5000, j, 10+j)...)
			}
			buf = append(buf, "]}"...)
		}
		buf = append(buf, fmt.Sprintf(`],"total_count":%d}`, n)...)
		return buf
	}
}

// --------------------------------------------------- PERF: N+1 request counts

func TestPerfNPlusOneRequestCounts(t *testing.T) {
	ctx := context.Background()

	t.Run("import_create_10_rows", func(t *testing.T) {
		f := &fake{rec: newRecorder()}
		f.rec.missingSKUs = map[string]bool{}
		for i := 0; i < 10; i++ {
			f.rec.missingSKUs[fmt.Sprintf("SKU-%02d", i)] = true
		}
		svc := mustService(t, f)
		var b strings.Builder
		b.WriteString("sku,name,price,attribute_set_id,type_id\n")
		for i := 0; i < 10; i++ {
			fmt.Fprintf(&b, "SKU-%02d,Product %d,9.99,4,simple\n", i, i)
		}
		res, err := svc.ExecuteImport(ctx, "csv", []byte(b.String()), false)
		if err != nil {
			t.Fatalf("ExecuteImport: %v", err)
		}
		total, byKey := f.rec.snapshot()
		report(t, "import create 10 rows", total, byKey)
		t.Logf("PROBE import result created=%d failed=%d", res.Created, res.Failed)
		if total != 20 {
			t.Errorf("want 20 requests (1 GET + 1 POST per row), got %d", total)
		}
	})

	t.Run("import_update_10_rows", func(t *testing.T) {
		f := &fake{rec: newRecorder()}
		svc := mustService(t, f)
		var b strings.Builder
		b.WriteString("sku,name,price,attribute_set_id,type_id\n")
		for i := 0; i < 10; i++ {
			fmt.Fprintf(&b, "SKU-%02d,Product %d,9.99,4,simple\n", i, i)
		}
		res, err := svc.ExecuteImport(ctx, "csv", []byte(b.String()), true)
		if err != nil {
			t.Fatalf("ExecuteImport: %v", err)
		}
		total, byKey := f.rec.snapshot()
		report(t, "import update 10 rows", total, byKey)
		t.Logf("PROBE import result updated=%d failed=%d", res.Updated, res.Failed)
		if total != 20 {
			t.Errorf("want 20 requests (1 GET + 1 PUT per row), got %d", total)
		}
	})

	t.Run("stock_statuses_25_skus", func(t *testing.T) {
		f := &fake{rec: newRecorder()}
		svc := mustService(t, f)
		skus := make([]string, 25)
		for i := range skus {
			skus[i] = fmt.Sprintf("SKU-%02d", i)
		}
		if _, err := svc.StockStatuses(ctx, skus); err != nil {
			t.Fatalf("StockStatuses: %v", err)
		}
		total, byKey := f.rec.snapshot()
		report(t, "stock statuses 25 SKUs", total, byKey)
		if total != 25 {
			t.Errorf("want 25 requests, got %d", total)
		}
	})

	t.Run("bulk_update_50_skus", func(t *testing.T) {
		f := &fake{rec: newRecorder()}
		svc := mustService(t, f)
		skus := make([]string, 50)
		for i := range skus {
			skus[i] = fmt.Sprintf("SKU-%02d", i)
		}
		status := 2
		if _, err := svc.BulkUpdateProducts(ctx, skus, magento.UpdateProductInput{Status: &status}); err != nil {
			t.Fatalf("BulkUpdateProducts: %v", err)
		}
		total, byKey := f.rec.snapshot()
		report(t, "bulk update 50 SKUs", total, byKey)
		if total != 50 {
			t.Errorf("want 50 requests, got %d", total)
		}
	})

	t.Run("bulk_options_10_labels", func(t *testing.T) {
		f := &fake{rec: newRecorder()}
		svc := mustService(t, f)
		labels := make([]string, 10)
		for i := range labels {
			labels[i] = fmt.Sprintf("Color %d", i)
		}
		if _, err := svc.BulkCreateAttributeOptions(ctx, "color", labels); err != nil {
			t.Fatalf("BulkCreateAttributeOptions: %v", err)
		}
		total, byKey := f.rec.snapshot()
		report(t, "bulk attribute options 10 labels", total, byKey)
		if total != 10 {
			t.Errorf("want 10 requests, got %d", total)
		}
	})

	t.Run("create_configurable_10_children", func(t *testing.T) {
		f := &fake{rec: newRecorder()}
		svc := mustService(t, f)
		qty := 3
		children := make([]magento.ChildProduct, 10)
		for i := range children {
			children[i] = magento.ChildProduct{
				SKU:        fmt.Sprintf("CHILD-%02d", i),
				Attributes: map[string]string{"color": strconv.Itoa(10 + i)},
				Quantity:   &qty,
			}
		}
		_, err := svc.CreateConfigurable(ctx, magento.CreateConfigurableInput{
			Parent: magento.CreateProductInput{
				SKU: "PARENT-1", Name: "Parent", AttributeSetID: 4, Price: "10.00",
			},
			Variants: []magento.VariantAttribute{{
				AttributeID: 93, Code: "color", Values: variantValues(10),
			}},
			Children: children,
		})
		if err != nil {
			t.Fatalf("CreateConfigurable: %v", err)
		}
		total, byKey := f.rec.snapshot()
		report(t, "create configurable 10 children", total, byKey)
	})
}

// variantValues returns n distinct option value indexes starting at first.
func variantValues(first int) []magento.VariantValue {
	out := make([]magento.VariantValue, 10)
	for i := range out {
		out[i] = magento.VariantValue{ValueIndex: first + i}
	}
	return out
}

// ------------------------------------------------ PERF: pagination at scale

func TestPerfOrderPaginationScenarios(t *testing.T) {
	ctx := context.Background()
	from := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

	scenarios := []struct {
		name      string
		orders    int
		maxOrders int // 0 => default
		geography bool
	}{
		{name: "sales_summary_924", orders: 924},
		{name: "sales_summary_20000", orders: 20000},
		{name: "analysis_924", orders: 924, maxOrders: 20000},
		{name: "analysis_20000", orders: 20000, maxOrders: 20000},
		{name: "geography_20000_cap10000", orders: 20000, maxOrders: 10000, geography: true},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			f := &fake{rec: newRecorder(), pages: orderPages(sc.orders)}
			svc := mustService(t, f)
			switch {
			case sc.geography:
				got, err := svc.OrderGeography(ctx, from, to, sc.maxOrders)
				if err != nil {
					t.Fatalf("OrderGeography: %v", err)
				}
				total, byKey := f.rec.snapshot()
				report(t, sc.name, total, byKey)
				t.Logf("PROBE geography orders_scanned=%d total_available=%d truncated=%v",
					got.OrdersScanned, got.TotalAvailable, got.Truncated)
			case sc.maxOrders > 0:
				got, err := svc.AnalyzeSales(ctx, from, to, sc.maxOrders, "UTC")
				if err != nil {
					t.Fatalf("AnalyzeSales: %v", err)
				}
				total, byKey := f.rec.snapshot()
				report(t, sc.name, total, byKey)
				t.Logf("PROBE analysis orders=%d total_available=%d truncated=%v top_skus=%d",
					got.Orders, got.TotalAvailable, got.Truncated, len(got.TopSKUs))
			default:
				got, err := svc.SalesSummary(ctx, from, to)
				if err != nil {
					t.Fatalf("SalesSummary: %v", err)
				}
				total, byKey := f.rec.snapshot()
				report(t, sc.name, total, byKey)
				t.Logf("PROBE summary scanned_orders=%d total_orders=%d truncated=%v days=%d",
					got.ScannedOrders, got.TotalOrders, got.Truncated, len(got.Days))
			}
		})
	}
}

// ------------------------------------------------- PERF: 8 MiB response cap

func TestPerfResponseTruncation(t *testing.T) {
	// The client reads at most maxResponseBytes (8 MiB) with io.LimitReader and
	// never reports that it truncated.
	sizes := []int{8<<20 - 1024, 8<<20 + 1024}
	for _, size := range sizes {
		size := size
		t.Run(fmt.Sprintf("body_%d_bytes", size), func(t *testing.T) {
			body := make([]byte, 0, size+64)
			body = append(body, `{"id":1,"sku":"BIG","name":"`...)
			body = append(body, strings.Repeat("a", size-len(body)-16)...)
			body = append(body, `"}`...)
			f := &fake{rec: newRecorder()}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				f.rec.hit("GET /products/{sku}")
				writeRaw(w, body)
			}))
			t.Cleanup(srv.Close)
			svc := newService(t, srv.URL, nil)
			product, err := svc.Get(context.Background(), "BIG")
			if err != nil {
				t.Logf("PROBE response %d bytes -> error: %v", len(body), err)
				return
			}
			t.Logf("PROBE response %d bytes -> ok, name_len=%d", len(body), len(product.Name))
		})
	}
}

// ------------------------------------------- PERF: connection pool behaviour

// TestPerfConnectionChurn measures how many TCP connections one client opens
// for a burst of concurrent, sequential requests. The production client is
// built without a Transport, so it inherits http.DefaultTransport
// (MaxIdleConnsPerHost = 2).
func TestPerfConnectionChurn(t *testing.T) {
	const (
		workers   = 8
		perWorker = 20
		think     = time.Millisecond
	)
	run := func(t *testing.T, tuned bool) (int32, time.Duration) {
		f := &fake{rec: newRecorder()}
		srv := httptest.NewUnstartedServer(f)
		srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
			if s == http.StateNew {
				atomic.AddInt32(&f.conns, 1)
			}
		}
		srv.StartTLS()
		t.Cleanup(srv.Close)
		var httpClient *http.Client
		if tuned {
			httpClient = &http.Client{Transport: &http.Transport{
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, // test server cert
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 64,
				IdleConnTimeout:     90 * time.Second,
			}}
		} else {
			httpClient = &http.Client{Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // test server cert
			}}
		}
		svc := newService(t, srv.URL, httpClient)
		start := time.Now()
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func(w int) {
				defer wg.Done()
				for i := 0; i < perWorker; i++ {
					if _, err := svc.StockStatus(context.Background(), fmt.Sprintf("SKU-%02d-%02d", w, i)); err != nil {
						t.Errorf("StockStatus: %v", err)
						return
					}
					time.Sleep(think)
				}
			}(w)
		}
		wg.Wait()
		return atomic.LoadInt32(&f.conns), time.Since(start)
	}

	defConns, defDur := run(t, false)
	tunedConns, tunedDur := run(t, true)
	requests := int32(workers * perWorker)
	t.Logf("PROBE conn churn default-transport requests=%d conns=%d elapsed=%s", requests, defConns, defDur.Round(time.Millisecond))
	t.Logf("PROBE conn churn tuned-transport   requests=%d conns=%d elapsed=%s", requests, tunedConns, tunedDur.Round(time.Millisecond))
	t.Logf("PROBE conn churn handshake_overhead_per_request=%d%%",
		int(100*(defConns-tunedConns)/requests))
}

// -------------------------------------------------- PERF: import throughput

// TestPerfImportThroughput measures the wall-clock cost of one import with a
// fixed per-request latency, then extrapolates the largest allowed batch.
func TestPerfImportThroughput(t *testing.T) {
	const (
		rows    = 50
		latency = 2 * time.Millisecond
	)
	f := &fake{rec: newRecorder(), latency: latency}
	f.rec.missingSKUs = map[string]bool{}
	for i := 0; i < rows; i++ {
		f.rec.missingSKUs[fmt.Sprintf("SKU-%03d", i)] = true
	}
	svc := mustService(t, f)
	var b strings.Builder
	b.WriteString("sku,name,price,attribute_set_id,type_id\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "SKU-%03d,Product %d,9.99,4,simple\n", i, i)
	}
	start := time.Now()
	res, err := svc.ExecuteImport(context.Background(), "csv", []byte(b.String()), false)
	if err != nil {
		t.Fatalf("ExecuteImport: %v", err)
	}
	elapsed := time.Since(start)
	total, _ := f.rec.snapshot()
	perRow := elapsed / rows
	t.Logf("PROBE import throughput rows=%d requests=%d created=%d elapsed=%s per_row=%s",
		rows, total, res.Created, elapsed.Round(time.Millisecond), perRow.Round(time.Microsecond))
	for _, lat := range []time.Duration{50 * time.Millisecond, 80 * time.Millisecond, 150 * time.Millisecond} {
		t.Logf("PROBE import extrapolation latency=%s rows=5000 requests=10000 wall=%s",
			lat, (2 * lat * 5000).Round(time.Second))
	}
}

// ---------------------------------------------- PERF: analysis allocation

// TestPerfAnalysisAllocation measures client-side allocation and payload volume
// for the largest allowed AnalyzeSales scan (20,000 orders x 4 line items).
func TestPerfAnalysisAllocation(t *testing.T) {
	const orders = 20000
	pages := orderPages(orders)
	// Precompute the page bodies so server-side marshalling does not pollute the
	// client-side allocation measurement.
	cache := map[int][]byte{}
	var payloadBytes int
	for page := 1; page <= orders/500+1; page++ {
		cache[page] = pages(page, 500)
		payloadBytes += len(cache[page])
	}
	f := &fake{rec: newRecorder()}
	f.pages = func(page, size int) []byte { return cache[page] }
	svc := mustService(t, f)

	from := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	got, err := svc.AnalyzeSales(context.Background(), from, to, orders, "UTC")
	if err != nil {
		t.Fatalf("AnalyzeSales: %v", err)
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	total, _ := f.rec.snapshot()
	t.Logf("PROBE analysis alloc orders=%d requests=%d payload=%d bytes total_alloc=%d bytes peak_heap=%d bytes elapsed=%s",
		got.Orders, total, payloadBytes, after.TotalAlloc-before.TotalAlloc, after.HeapAlloc, elapsed.Round(time.Millisecond))
	t.Logf("PROBE analysis result by_day=%d top_skus=%d currencies=%d", len(got.ByDay), len(got.TopSKUs), len(got.Currencies))
}

// TestPerfImportPayloadFootprint measures the import payload ceiling and the
// limit it is now reconciled with. PERF-12 fixed the mismatch by deriving the
// accepted payload from the transport body cap; this probe records the new
// numbers and asserts the reconciliation still holds.
func TestPerfImportPayloadFootprint(t *testing.T) {
	const rows = 5000
	description := strings.Repeat("d", 300)
	row := func(i int) string {
		return fmt.Sprintf("SKU-%05d,Product %d,19.99,4,simple,%s\n", i, i, description)
	}
	var b strings.Builder
	b.WriteString("sku,name,price,attribute_set_id,type_id,description\n")
	for i := 0; i < rows; i++ {
		b.WriteString(row(i))
	}
	data := []byte(b.String())
	base64Len := (len(data) + 2) / 3 * 4
	t.Logf("PROBE import payload rows=%d csv_bytes=%d base64_bytes=%d", rows, len(data), base64Len)
	t.Logf("PROBE import payload default_http_body_limit=1048576 fits_http=%v", base64Len <= 1<<20)
	t.Logf("PROBE import limit declared_decoded_bytes=%d encoded_bytes=%d",
		magento.MaxImportBytes, (magento.MaxImportBytes+2)/3*4)

	svc := mustService(t, &fake{rec: newRecorder()})
	if _, err := svc.ValidateImport(context.Background(), "csv", data); err == nil {
		t.Fatalf("a %d-byte payload must be rejected once the limit is reconciled with the body cap", len(data))
	} else {
		t.Logf("PROBE import oversized payload rejected: %v", err)
	}

	// Largest row count of this shape that still fits the declared limit.
	header := "sku,name,price,attribute_set_id,type_id,description\n"
	var fitting strings.Builder
	fitting.WriteString(header)
	fit := 0
	for fit < rows {
		next := row(fit)
		if fitting.Len()+len(next) > magento.MaxImportBytes {
			break
		}
		fitting.WriteString(next)
		fit++
	}
	t.Logf("PROBE import rows_that_fit rows=%d csv_bytes=%d", fit, fitting.Len())

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	preview, err := svc.ValidateImport(context.Background(), "csv", []byte(fitting.String()))
	if err != nil {
		t.Fatalf("ValidateImport: %v", err)
	}
	runtime.ReadMemStats(&after)
	t.Logf("PROBE import parse rows=%d valid=%d total_alloc=%d bytes peak_heap=%d bytes",
		preview.Total, preview.Valid, after.TotalAlloc-before.TotalAlloc, after.HeapAlloc)

	encoded := make([]byte, (fitting.Len()+2)/3*4)
	base64.StdEncoding.Encode(encoded, []byte(fitting.String()))
	decoded, err := magento.DecodeImportPayload(string(encoded))
	if err != nil {
		t.Fatalf("DecodeImportPayload: %v", err)
	}
	t.Logf("PROBE import decode base64_bytes=%d decoded_bytes=%d", len(encoded), len(decoded))
	if got := len(encoded) + 4096; got > magento.DefaultHTTPBodyBytes {
		t.Fatalf("encoded payload plus envelope = %d bytes, over the %d-byte body cap", got, magento.DefaultHTTPBodyBytes)
	}
}

// -------------------------------------------------------------- benchmarks

func BenchmarkStockStatuses25(b *testing.B) {
	f := &fake{rec: newRecorder()}
	srv := startFakeB(b, f)
	svc := newServiceB(b, srv.URL, nil)
	skus := make([]string, 25)
	for i := range skus {
		skus[i] = fmt.Sprintf("SKU-%02d", i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.StockStatuses(context.Background(), skus); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnalyzeSales924(b *testing.B) {
	f := &fake{rec: newRecorder(), pages: orderPages(924)}
	srv := startFakeB(b, f)
	svc := newServiceB(b, srv.URL, nil)
	from := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.AnalyzeSales(context.Background(), from, to, 20000, "UTC"); err != nil {
			b.Fatal(err)
		}
	}
}

// startFakeB / newServiceB mirror the *testing.T helpers for benchmarks.
func startFakeB(b *testing.B, f *fake) *httptest.Server {
	b.Helper()
	if f.rec == nil {
		f.rec = newRecorder()
	}
	if f.ship == nil {
		f.ship = []byte(`{"items":[],"total_count":0}`)
	}
	if f.product == nil {
		f.product = productJSON("created")
	}
	if f.pages == nil {
		f.pages = func(int, int) []byte { return []byte(`{"items":[],"total_count":0}`) }
	}
	srv := httptest.NewServer(f)
	b.Cleanup(srv.Close)
	return srv
}

func newServiceB(b *testing.B, baseURL string, httpClient *http.Client) *magento.Service {
	b.Helper()
	signer, err := magento.NewOAuth1Signer("key", "secret", "token", "token-secret")
	if err != nil {
		b.Fatalf("signer: %v", err)
	}
	client, err := magento.NewClient(magento.ClientConfig{
		BaseURL: baseURL, StoreCode: "default", Signer: signer, HTTPClient: httpClient,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		b.Fatalf("client: %v", err)
	}
	svc, err := magento.NewService(client)
	if err != nil {
		b.Fatalf("service: %v", err)
	}
	return svc
}
