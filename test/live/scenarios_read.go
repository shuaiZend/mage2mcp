// SPDX-License-Identifier: Apache-2.0

//go:build live

package main

import (
	"fmt"
	"os"
	"time"
)

// ts is the run stamp used to build unique test identifiers.
var ts = time.Now().Format("20060102-150405")

// skuPrefix marks every object this harness creates.
const skuPrefix = "MCPTEST-"

// readCases exercise every read-only tool plus the error semantics, and record
// the ids the write cases depend on.
func readCases() []Case {
	return []Case{
		{
			Group: "platform", Tool: "platform.info", Case: "reports the registered surface",
			Check: func(_ *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				tools, _ := out["tools"].([]any)
				if len(tools) != 102 {
					return fmt.Errorf("tools = %d, want 102", len(tools))
				}
				if enabled, _ := out["writes_enabled"].(bool); !enabled {
					return fmt.Errorf("writes_enabled = false, want true")
				}
				return nil
			},
		},
		{
			Group: "platform", Tool: "platform.info", Case: "lists itself",
			Check: func(_ *Env, res *ToolResult) error {
				out, _ := output(res)
				tools, _ := out["tools"].([]any)
				for _, t := range tools {
					if t == "platform.info" {
						return nil
					}
				}
				return fmt.Errorf("platform.info missing from its own tools list")
			},
		},
		{Group: "store", Tool: "magento.store.configs", Case: "reads store views",
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				// The shape is {stores:[...]}; accept either a top-level array or a key.
				if _, ok := out["stores"]; !ok {
					return fmt.Errorf("no stores key in output")
				}
				return nil
			}},
		{Group: "store", Tool: "magento.tax.classes", Case: "reads tax classes",
			Check: func(_ *Env, res *ToolResult) error {
				if res.Text() == "" && len(res.StructuredContent) == 0 {
					return fmt.Errorf("empty result")
				}
				return nil
			}},

		{Group: "catalog-read", Tool: "magento.attribute.sets", Case: "lists attribute sets"},
		{Group: "catalog-read", Tool: "magento.attributes.list", Case: "lists attributes of set 4",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "attribute_set_id": 4}, nil
			}},
		{Group: "catalog-read", Tool: "magento.attributes.options", Case: "lists color options",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "attribute_code": "color"}, nil
			}, Soft: true},
		{Group: "catalog-read", Tool: "magento.categories.flat", Case: "flattens the category tree"},
		{Group: "catalog-read", Tool: "magento.category.tree", Case: "reads the tree",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "depth": 2}, nil
			}},
		{Group: "catalog-read", Tool: "magento.category.get", Case: "reads the root category",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "category_id": 2}, nil
			}},
		{Group: "catalog-read", Tool: "magento.category.products", Case: "lists products of the root category",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "category_id": 2, "limit": 3}, nil
			}},

		{Group: "product-read", Tool: "magento.product.get", Case: "reads a known SKU",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("seed_sku")}, nil
			}, Need: []string{"seed_sku"}},
		{Group: "product-read", Tool: "magento.products.get", Case: "reads several SKUs in one call",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "skus": []string{e.Get("seed_sku")}}, nil
			}, Need: []string{"seed_sku"}},
		{Group: "product-read", Tool: "magento.product.search", Case: "searches by SKU prefix",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("seed_sku"), "limit": 3}, nil
			}, Need: []string{"seed_sku"}},
		{Group: "product-read", Tool: "magento.product.seo", Case: "audits SEO content",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("seed_sku")}, nil
			}, Need: []string{"seed_sku"}},
		{Group: "product-read", Tool: "magento.product.tier_prices", Case: "reads tier prices",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("seed_sku")}, nil
			}, Need: []string{"seed_sku"}},
		{Group: "product-read", Tool: "magento.product.media", Case: "lists media entries",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("seed_sku")}, nil
			}, Need: []string{"seed_sku"}},
		{Group: "product-read", Tool: "magento.product.options", Case: "lists custom options",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("seed_sku")}, nil
			}, Need: []string{"seed_sku"}},
		{Group: "product-read", Tool: "magento.product.links", Case: "lists related links",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("seed_sku"), "link_type": "related"}, nil
			}, Need: []string{"seed_sku"}},
		{Group: "product-read", Tool: "magento.product.media.types", Case: "reads media types",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "attribute_set_name": "Default"}, nil
			}},
		{Group: "configurable-read", Tool: "magento.configurable.children", Case: "lists children of a configurable",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "parent_sku": e.Get("seed_configurable")}, nil
			}, Need: []string{"seed_configurable"}, Soft: true},

		{Group: "inventory-read", Tool: "magento.inventory.sources", Case: "lists MSI sources"},
		{Group: "inventory-read", Tool: "magento.inventory.stocks", Case: "lists stocks"},
		{Group: "inventory-read", Tool: "magento.inventory.source_items", Case: "reads source items",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "skus": []string{e.Get("seed_sku")}}, nil
			}, Need: []string{"seed_sku"}},
		{Group: "inventory-read", Tool: "magento.inventory.status", Case: "reads stock status",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("seed_sku")}, nil
			}, Need: []string{"seed_sku"}},
		{Group: "inventory-read", Tool: "magento.inventory.status_batch", Case: "reads status in batch",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "skus": []string{e.Get("seed_sku")}}, nil
			}, Need: []string{"seed_sku"}},

		{Group: "customer-read", Tool: "magento.customer.groups", Case: "lists customer groups"},
		{Group: "customer-read", Tool: "magento.customer.search", Case: "searches a non-existent email",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "email": "nobody-" + ts + "@example.com"}, nil
			}},

		{Group: "sales-read", Tool: "magento.order.search", Case: "searches recent orders",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "limit": 3}, nil
			},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				items, _ := out["orders"].([]any)
				if len(items) == 0 {
					items, _ = out["items"].([]any)
				}
				if len(items) == 0 {
					return fmt.Errorf("no orders returned; cannot seed order-dependent cases")
				}
				if first, ok := items[0].(map[string]any); ok {
					if id, ok := num(first, "entity_id"); ok {
						e.Set("seed_order", fmt.Sprintf("%.0f", id))
					}
				}
				return nil
			}},
		{Group: "sales-read", Tool: "magento.order.get", Case: "reads a known order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("seed_order")}, nil
			}, Need: []string{"seed_order"}, Soft: true},
		{Group: "sales-read", Tool: "magento.order.status_history", Case: "reads status history",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("seed_order")}, nil
			}, Need: []string{"seed_order"}, Soft: true},
		{Group: "sales-read", Tool: "magento.order.summary", Case: "summarises a window",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "from": windowFrom(), "to": windowTo()}, nil
			}},
		{Group: "sales-read", Tool: "magento.sales.summary", Case: "summarises sales",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "from": windowFrom(), "to": windowTo()}, nil
			}},
		{Group: "sales-read", Tool: "magento.sales.analysis", Case: "analyses sales",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "from": windowFrom(), "to": windowTo(), "max_orders": 500}, nil
			}},
		{Group: "sales-read", Tool: "magento.orders.geography", Case: "aggregates geography",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "from": windowFrom(), "to": windowTo(), "max_orders": 500}, nil
			}},
		{Group: "sales-read", Tool: "magento.invoice.list", Case: "lists invoices of an order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("seed_order")}, nil
			}, Need: []string{"seed_order"}, Soft: true},
		{Group: "sales-read", Tool: "magento.creditmemo.list", Case: "lists credit memos of an order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("seed_order")}, nil
			}, Need: []string{"seed_order"}, Soft: true},
		{Group: "sales-read", Tool: "magento.shipment.list", Case: "lists shipments of an order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("seed_order")}, nil
			}, Need: []string{"seed_order"}, Soft: true},

		{Group: "import-read", Tool: "magento.import.validate", Case: "validates a small CSV",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "format": "csv", "data_base64": b64(importCSV("VALIDATE"))}, nil
			},
			Check: func(_ *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				if v, ok := num(out, "valid"); ok && v < 1 {
					return fmt.Errorf("valid rows = %.0f, want >= 1", v)
				}
				return nil
			}},
		{Group: "import-read", Tool: "magento.import.batches", Case: "lists import batches",
			Check: func(e *Env, res *ToolResult) error {
				// Keep the newest batch id so import.batch can be exercised with a
				// real batch rather than the job id.
				out, err := output(res)
				if err != nil {
					return err
				}
				batches, _ := out["batches"].([]any)
				if len(batches) > 0 {
					if first, ok := batches[0].(map[string]any); ok {
						if id := str(first, "id"); id != "" {
							e.Set("batch_id", id)
						}
					}
				}
				return nil
			}},

		{Group: "errors", Tool: "magento.product.get", Case: "unknown instance is not_found",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": "does-not-exist", "sku": e.Get("seed_sku")}, nil
			}, Need: []string{"seed_sku"}, WantCode: "not_found"},
		{Group: "errors", Tool: "magento.product.get", Case: "unknown SKU is not_found",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": "MCPTEST-DOES-NOT-EXIST-" + ts}, nil
			}, WantCode: "not_found"},
		{Group: "errors", Tool: "magento.product.create", Case: "missing required fields is invalid_argument",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": "X"}, nil
			}, WantCode: "invalid_argument"},
		{Group: "errors", Tool: "magento.inventory.transfer", Case: "same source and destination is rejected",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("seed_sku"),
					"from_source": "default", "to_source": "default", "quantity": 1,
				}, nil
			}, Need: []string{"seed_sku"}, Soft: true},
	}
}

// windowFrom and windowTo bound the sales queries: the last 180 days.
func windowFrom() string { return time.Now().AddDate(0, -6, 0).Format("2006-01-02") }
func windowTo() string   { return time.Now().Format("2006-01-02") }

// b64 is a tiny helper so scenarios read cleanly.
func b64(s string) string { return base64Encode(s) }

// importCSV builds a one-row import payload with a unique SKU.
func importCSV(tag string) string {
	return "sku,name,type_id,price,attribute_set_id\n" +
		fmt.Sprintf("%sIMP-%s-%s,Import %s,simple,9.99,4\n", skuPrefix, tag, ts, tag)
}

// importCSVWithFailure adds one row that is structurally valid but cannot be created, so
// the resulting batch always has failed rows. import.resume is only meaningful when
// something failed: without this the case flipped between "resumed" and "correctly
// refused" depending on whether the clean import happened to fail, which made the matrix
// change between runs for a reason unrelated to the code.
//
// The row must survive structural validation — an invalid type_id makes import.start
// refuse the whole payload ("import has invalid rows") rather than record a per-row
// failure. A well-formed row pointing at an attribute set that does not exist passes
// validation and fails at the Magento create, which is the failure the batch is for.
func importCSVWithFailure(tag string) string {
	return importCSV(tag) +
		fmt.Sprintf("%sIMP-%s-%s-BAD,Import %s Bad,simple,9.99,9999\n", skuPrefix, tag, ts, tag)
}

// seedReadEnv records the seed SKUs the read cases depend on, by searching the
// store once before the read matrix runs.
func seedReadEnv(e *Env) error {
	res, err := e.client.Call("magento.product.search", map[string]any{"instance": instance(), "limit": 1})
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("product.search: %s", res.Text())
	}
	out, err := output(res)
	if err != nil {
		return err
	}
	items, _ := out["products"].([]any)
	if len(items) == 0 {
		items, _ = out["items"].([]any)
	}
	if len(items) == 0 {
		return fmt.Errorf("store returned no products to seed from")
	}
	if first, ok := items[0].(map[string]any); ok {
		if sku := str(first, "sku"); sku != "" {
			e.Set("seed_sku", sku)
		}
	}
	if e.Get("seed_sku") == "" {
		return fmt.Errorf("could not determine a seed SKU")
	}

	// Find a configurable product for the children case, if the store has one.
	res, err = e.client.Call("magento.product.search", map[string]any{
		"instance": instance(), "type_id": "configurable", "limit": 1,
	})
	if err == nil && !res.IsError {
		if out, err := output(res); err == nil {
			items, _ := out["products"].([]any)
			if len(items) == 0 {
				items, _ = out["items"].([]any)
			}
			if len(items) > 0 {
				if first, ok := items[0].(map[string]any); ok {
					e.Set("seed_configurable", str(first, "sku"))
				}
			}
		}
	}
	fmt.Fprintf(os.Stderr, "seeded: sku=%s configurable=%s\n", e.Get("seed_sku"), e.Get("seed_configurable"))
	return nil
}
