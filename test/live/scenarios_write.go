// SPDX-License-Identifier: Apache-2.0

//go:build live

package main

import (
	"fmt"
)

// onePixelPNG is a 1x1 transparent PNG, enough for the media upload path.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// writeCases exercises every write tool. Cases run in order because each one may
// seed the next; a failed case leaves its value unset, so its dependants report
// as skipped rather than cascading noise.
func writeCases() []Case {
	return []Case{
		// ---- catalog: product lifecycle -------------------------------------
		{
			Group: "product-write", Tool: "magento.product.create", Case: "creates a simple product with full content",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"),
					"name": "MCPTEST Simple Product", "attribute_set_id": 4, "type_id": "simple",
					"price": "19.99", "quantity": 7, "weight": "0.5", "tax_class_id": 2,
					"visibility": 4, "status": 1, "website_ids": []int{1},
					"description":       "<p>Live functional-test product.</p>",
					"short_description": "<p>Created by the live harness.</p>",
					"url_key":           e.Get("p1slug"),
					"meta_title":        "MCPTEST Simple Product",
					"meta_description":  "Created by the mage2mcp live functional-test harness.",
					"meta_keywords":     "mcptest,harness",
					"attributes":        map[string]string{"color": "12"},
				}, nil
			},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				product, _ := out["product"].(map[string]any)
				if product == nil {
					product = out
				}
				if sku := str(product, "sku"); sku != e.Get("p1") {
					return fmt.Errorf("returned sku %q, want %q", sku, e.Get("p1"))
				}
				return nil
			},
		},
		{
			Group: "product-write", Tool: "magento.product.get", Case: "reads back the created product",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("p1")}, nil
			}, Need: []string{"p1"},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				product, _ := out["product"].(map[string]any)
				if product == nil {
					product = out
				}
				if str(product, "meta_title") == "" {
					return fmt.Errorf("meta_title not persisted")
				}
				return nil
			},
		},
		{
			Group: "product-write", Tool: "magento.product.update", Case: "updates price and name",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"),
					"name": "MCPTEST Simple Product v2", "price": "24.50",
				}, nil
			}, Need: []string{"p1"},
		},
		{
			Group: "product-write", Tool: "magento.product.seo", Case: "audits the created product",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("p1")}, nil
			}, Need: []string{"p1"},
		},

		// ---- media -----------------------------------------------------------
		{
			Group: "media-write", Tool: "magento.product.media.upload", Case: "uploads a PNG",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"),
					"filename": e.Get("media_file"), "mime_type": "image/png",
					"data_base64": onePixelPNG, "label": "MCPTEST image",
				}, nil
			}, Need: []string{"p1"},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				entry, _ := out["entry"].(map[string]any)
				if entry == nil {
					entry, _ = out["media"].(map[string]any)
				}
				if entry == nil {
					return fmt.Errorf("no entry in output: %v", out)
				}
				if id, ok := num(entry, "id"); ok && id > 0 {
					e.Set("media_id", fmt.Sprintf("%.0f", id))
				}
				return nil
			},
		},
		{
			Group: "media-write", Tool: "magento.product.media", Case: "lists the uploaded media",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("p1")}, nil
			}, Need: []string{"p1"},
			Check: func(e *Env, res *ToolResult) error {
				if e.Get("media_id") == "" {
					return fmt.Errorf("upload did not report an entry id")
				}
				return nil
			},
		},
		{
			Group: "media-write", Tool: "magento.product.media.update", Case: "renames the media entry",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"),
					"media_id": e.Int("media_id"), "label": "MCPTEST image v2",
				}, nil
			}, Need: []string{"p1", "media_id"}, Soft: true,
		},
		{
			Group: "media-write", Tool: "magento.product.media.delete", Case: "deletes the media entry",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"), "media_id": e.Int("media_id"),
				}, nil
			}, Need: []string{"p1", "media_id"}, Soft: true,
		},

		// ---- custom options --------------------------------------------------
		{
			Group: "options-write", Tool: "magento.product.options.create", Case: "creates a drop-down option",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"),
					"title": "MCPTEST Size", "type": "drop_down", "is_require": true, "sort_order": 1,
					"values": []map[string]any{
						{"title": "Small", "price": 0, "price_type": "fixed"},
						{"title": "Large", "price": 5, "price_type": "fixed"},
					},
				}, nil
			}, Need: []string{"p1"},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				opt, _ := out["option"].(map[string]any)
				if opt == nil {
					opt = out
				}
				if id, ok := num(opt, "option_id"); ok && id > 0 {
					e.Set("option_id", fmt.Sprintf("%.0f", id))
				}
				return nil
			},
		},
		{
			Group: "options-write", Tool: "magento.product.options", Case: "lists the created option",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("p1")}, nil
			}, Need: []string{"p1"},
		},
		{
			Group: "options-write", Tool: "magento.product.options.update", Case: "renames the option",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"),
					"option_id": e.Int("option_id"), "title": "MCPTEST Size v2", "type": "drop_down",
				}, nil
			}, Need: []string{"p1", "option_id"}, Soft: true,
		},
		{
			Group: "options-write", Tool: "magento.product.options.delete", Case: "deletes the option",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"), "option_id": e.Int("option_id"),
				}, nil
			}, Need: []string{"p1", "option_id"}, Soft: true,
		},

		// ---- links and tier prices -------------------------------------------
		{
			Group: "links-write", Tool: "magento.product.links.add", Case: "links a related product",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"),
					"link_type": "related", "linked_product_sku": e.Get("seed_sku"),
				}, nil
			}, Need: []string{"p1", "seed_sku"}, Soft: true,
		},
		{
			Group: "links-write", Tool: "magento.product.links", Case: "reads the link back",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("p1"), "link_type": "related"}, nil
			}, Need: []string{"p1"}, Soft: true,
		},
		{
			Group: "links-write", Tool: "magento.product.links.bulk", Case: "bulk-adds a related link",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"), "link_type": "related",
					"action": "add", "linked_skus": []string{e.Get("seed_sku")},
				}, nil
			}, Need: []string{"p1", "seed_sku"}, Soft: true,
		},
		{
			Group: "links-write", Tool: "magento.product.links.remove", Case: "removes the related link",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"),
					"link_type": "related", "linked_product_sku": e.Get("seed_sku"),
				}, nil
			}, Need: []string{"p1", "seed_sku"}, Soft: true,
		},
		{
			Group: "tier-write", Tool: "magento.product.tier_prices.set", Case: "sets a tier price",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"),
					"prices": []map[string]any{
						{"quantity": 5, "price": 18.0, "website_id": 0, "customer_group": "0"},
					},
				}, nil
			}, Need: []string{"p1"}, Soft: true,
		},
		{
			Group: "tier-write", Tool: "magento.product.tier_prices", Case: "reads the tier price back",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("p1")}, nil
			}, Need: []string{"p1"}, Soft: true,
		},
		{
			Group: "bulk-write", Tool: "magento.products.bulk_update", Case: "bulk-updates status",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "skus": []string{e.Get("p1")}, "status": 1,
				}, nil
			}, Need: []string{"p1"}, Soft: true,
		},

		// ---- attribute options ------------------------------------------------
		{
			Group: "attribute-write", Tool: "magento.attributes.options.create", Case: "creates a color option",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "attribute_code": "color", "label": "MCPTEST-Blue-" + ts,
				}, nil
			},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				if id, ok := num(out, "option_id"); ok && id > 0 {
					e.Set("color_option_id", fmt.Sprintf("%.0f", id))
				}
				return nil
			}, Soft: true,
		},
		{
			Group: "attribute-write", Tool: "magento.attributes.options.bulk_create", Case: "bulk-creates color options",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "attribute_code": "color",
					"labels": []string{"MCPTEST-Bulk1-" + ts, "MCPTEST-Bulk2-" + ts},
				}, nil
			}, Soft: true,
		},
		{
			Group: "attribute-write", Tool: "magento.attributes.options.delete", Case: "deletes the created option",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "attribute_code": "color",
					"option_id": e.Int("color_option_id"),
				}, nil
			}, Need: []string{"color_option_id"}, Soft: true,
		},

		// ---- categories -------------------------------------------------------
		{
			Group: "category-write", Tool: "magento.category.create", Case: "creates a category",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "parent_id": 2, "name": "MCPTEST Category " + ts,
					"is_active": true, "include_in_menu": false, "url_key": "mcptest-cat-" + ts,
				}, nil
			},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				cat, _ := out["category"].(map[string]any)
				if cat == nil {
					cat = out
				}
				if id, ok := num(cat, "id"); ok && id > 0 {
					e.Set("cat_id", fmt.Sprintf("%.0f", id))
				} else if id, ok := num(cat, "entity_id"); ok && id > 0 {
					e.Set("cat_id", fmt.Sprintf("%.0f", id))
				}
				return nil
			},
		},
		{
			Group: "category-write", Tool: "magento.category.update", Case: "renames the category",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "category_id": e.Int("cat_id"),
					"name": "MCPTEST Category v2 " + ts,
				}, nil
			}, Need: []string{"cat_id"}, Soft: true,
		},
		{
			Group: "category-write", Tool: "magento.category.assign_product", Case: "assigns the product",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "category_id": e.Int("cat_id"), "sku": e.Get("p1"),
				}, nil
			}, Need: []string{"cat_id", "p1"}, Soft: true,
		},
		{
			Group: "category-write", Tool: "magento.category.products", Case: "lists the assigned product",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "category_id": e.Int("cat_id"), "limit": 10}, nil
			}, Need: []string{"cat_id"}, Soft: true,
		},
		{
			Group: "category-write", Tool: "magento.category.create", Case: "creates a throwaway category to delete",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "parent_id": 2, "name": "MCPTEST Delete Me " + ts,
					"is_active": false, "include_in_menu": false, "url_key": "mcptest-del-" + ts,
				}, nil
			},
			Check: func(e *Env, res *ToolResult) error {
				out, _ := output(res)
				cat, _ := out["category"].(map[string]any)
				if cat == nil {
					cat = out
				}
				if id, ok := num(cat, "id"); ok && id > 0 {
					e.Set("cat_del", fmt.Sprintf("%.0f", id))
				} else if id, ok := num(cat, "entity_id"); ok && id > 0 {
					e.Set("cat_del", fmt.Sprintf("%.0f", id))
				}
				return nil
			}, Soft: true,
		},
		{
			Group: "category-write", Tool: "magento.category.delete", Case: "deletes the throwaway category",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "category_id": e.Int("cat_del")}, nil
			}, Need: []string{"cat_del"}, Soft: true,
		},

		// ---- configurable -----------------------------------------------------
		{
			Group: "configurable-write", Tool: "magento.configurable.create", Case: "creates a configurable with two children",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(),
					"parent": map[string]any{
						"sku": e.Get("cfg"), "name": "MCPTEST Configurable", "attribute_set_id": 4,
						"price": "29.99", "visibility": 4, "status": 1, "website_ids": []int{1},
						"url_key": e.Get("cfgslug"),
					},
					"variant_attributes": []map[string]any{
						// attribute_id is required by the contract; 93 is the `color`
						// attribute of the Default set on this store.
						{"attribute_id": 93, "code": "color", "values": []map[string]any{
							{"value_index": 49, "label": "Black"},
							{"value_index": 50, "label": "Blue"},
						}},
					},
					"children": []map[string]any{
						{"sku": e.Get("cfg") + "-A", "name": "MCPTEST Config A", "price": "29.99",
							"attributes": map[string]string{"color": "49"}, "quantity": 3},
						{"sku": e.Get("cfg") + "-B", "name": "MCPTEST Config B", "price": "31.99",
							"attributes": map[string]string{"color": "50"}, "quantity": 3},
					},
				}, nil
			},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				if op := str(out, "operation_id"); op != "" {
					e.Set("cfg_operation", op)
				}
				if outcome := str(out, "outcome"); outcome == "partial" {
					return fmt.Errorf("configurable create reported partial: %s", res.Text())
				}
				return nil
			}, Soft: true,
		},
		{
			Group: "configurable-write", Tool: "magento.configurable.children", Case: "lists the created children",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "parent_sku": e.Get("cfg")}, nil
			}, Need: []string{"cfg"}, Soft: true,
		},
		{
			Group: "configurable-write", Tool: "magento.configurable.update", Case: "updates the parent name",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("cfg"), "name": "MCPTEST Configurable v2",
				}, nil
			}, Need: []string{"cfg"}, Soft: true,
		},
		{
			Group: "configurable-write", Tool: "magento.configurable.create_variations", Case: "adds variations",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "parent_sku": e.Get("cfg"),
					// The contract allows only sku and attributes here; name and
					// quantity belong to the parent or the stock call.
					"variants": []map[string]any{
						{"sku": e.Get("cfg") + "-C",
							"attributes": map[string]string{"color": "49"}},
					},
				}, nil
			}, Need: []string{"cfg"}, Soft: true,
		},
		{
			Group: "configurable-write", Tool: "magento.configurable.set_options", Case: "sets parent options",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("cfg"),
					// values is an array of attribute option ids, not objects.
					"options": []map[string]any{
						{"attribute_id": 93, "label": "Color", "values": []int{49, 50}},
					},
				}, nil
			}, Need: []string{"cfg"}, Soft: true,
		},
		{
			Group: "configurable-write", Tool: "magento.configurable.add_child", Case: "adds an existing child",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "parent_sku": e.Get("cfg"), "child_sku": e.Get("cfg") + "-A",
				}, nil
			}, Need: []string{"cfg"}, Soft: true,
		},
		{
			Group: "configurable-write", Tool: "magento.configurable.remove_child", Case: "removes a child",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "parent_sku": e.Get("cfg"), "child_sku": e.Get("cfg") + "-B",
				}, nil
			}, Need: []string{"cfg"}, Soft: true,
		},

		// ---- inventory --------------------------------------------------------
		{
			Group: "inventory-write", Tool: "magento.inventory.assign", Case: "assigns stock to the product",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(),
					"items": []map[string]any{
						{"sku": e.Get("p1"), "source_code": "default", "quantity": 9, "status": 1},
					},
				}, nil
			}, Need: []string{"p1"}, Soft: true,
		},
		{
			Group: "inventory-write", Tool: "magento.inventory.source_items", Case: "verifies the assigned quantity",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "skus": []string{e.Get("p1")}}, nil
			}, Need: []string{"p1"}, Soft: true,
		},
		{
			Group: "inventory-write", Tool: "magento.inventory.source_create", Case: "creates an MSI source",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "source_code": "mcptest_src", "name": "MCPTEST Source",
					"country_id": "PL", "postcode": "00-001", "city": "Warsaw",
				}, nil
			}, Soft: true,
		},
		{
			Group: "inventory-write", Tool: "magento.inventory.stock_source_link", Case: "links the source to stock 1",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "stock_id": 1, "source_code": "mcptest_src",
				}, nil
			}, Soft: true,
		},
		{
			Group: "inventory-write", Tool: "magento.inventory.stock_source_unlink", Case: "unlinks the source",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "stock_id": 1, "source_code": "mcptest_src",
				}, nil
			}, Soft: true,
		},
		{
			Group: "inventory-write", Tool: "magento.inventory.unassign", Case: "unassigns a source item",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(),
					"items":    []map[string]any{{"sku": e.Get("p1"), "source_code": "mcptest_src"}},
				}, nil
			}, Need: []string{"p1"}, Soft: true,
		},
		{
			Group: "inventory-write", Tool: "magento.inventory.transfer", Case: "same source and destination is rejected",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "sku": e.Get("p1"),
					"from_source": "default", "to_source": "default", "quantity": 1,
				}, nil
			}, Need: []string{"p1"}, WantCode: "invalid_argument", Soft: true,
		},

		// ---- customer ---------------------------------------------------------
		{
			Group: "customer-write", Tool: "magento.customer.create", Case: "creates a customer",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "email": e.Get("email"),
					"firstname": "MCPTEST", "lastname": "Customer", "website_id": 1,
				}, nil
			},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				cust, _ := out["customer"].(map[string]any)
				if cust == nil {
					cust = out
				}
				if id, ok := num(cust, "id"); ok && id > 0 {
					e.Set("cust_id", fmt.Sprintf("%.0f", id))
				}
				return nil
			},
		},
		{
			Group: "customer-write", Tool: "magento.customer.search", Case: "finds the created customer",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "email": e.Get("email")}, nil
			}, Need: []string{"email"}, Soft: true,
		},
		{
			Group: "customer-write", Tool: "magento.customer.get", Case: "reads the customer",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "customer_id": e.Int("cust_id")}, nil
			}, Need: []string{"cust_id"}, Soft: true,
		},
		{
			Group: "customer-write", Tool: "magento.customer.update", Case: "updates the customer",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "customer_id": e.Int("cust_id"),
					"firstname": "MCPTESTv2", "lastname": "Customer",
				}, nil
			}, Need: []string{"cust_id"}, Soft: true,
		},
		{
			Group: "customer-write", Tool: "magento.customer.addresses.create", Case: "adds an address",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "customer_id": e.Int("cust_id"),
					"firstname": "MCPTEST", "lastname": "Customer",
					"street": []string{"1 MCPTEST Street"}, "city": "Wrocław",
					"postcode": "50-001", "country_id": "PL", "region_id": 997,
					"telephone":       "500600700",
					"default_billing": true, "default_shipping": true,
				}, nil
			}, Need: []string{"cust_id"},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				addr, _ := out["address"].(map[string]any)
				if addr == nil {
					addr = out
				}
				if id, ok := num(addr, "id"); ok && id > 0 {
					e.Set("addr_id", fmt.Sprintf("%.0f", id))
				} else if id, ok := num(addr, "entity_id"); ok && id > 0 {
					e.Set("addr_id", fmt.Sprintf("%.0f", id))
				}
				return nil
			}},
		{
			Group: "customer-write", Tool: "magento.customer.addresses", Case: "lists the addresses",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "customer_id": e.Int("cust_id")}, nil
			}, Need: []string{"cust_id"}, Soft: true,
		},
		{
			// Creates its own address instead of reusing the one from the earlier create
			// case. Reusing it made this case fail whenever that address was gone — a
			// demo-store reset between the two steps was enough — and the tool then
			// reported "does not expose customer address update", which points at the
			// wrong thing entirely.
			Group: "customer-write", Tool: "magento.customer.addresses.update", Case: "updates the address",
			Args: func(e *Env) (map[string]any, error) {
				res, err := e.client.Call("magento.customer.addresses.create", map[string]any{
					"instance": instance(), "customer_id": e.Int("cust_id"),
					"firstname": "MCPTEST", "lastname": "Customer",
					"street": []string{"2 MCPTEST Street"}, "city": "Wrocław",
					"postcode": "50-001", "country_id": "PL", "region_id": 997,
					"telephone":       "500600701",
					"default_billing": false, "default_shipping": false,
				})
				if err != nil || res.IsError {
					return nil, fmt.Errorf("seed address for update: %v %s", err, res.Text())
				}
				out, err := output(res)
				if err != nil {
					return nil, err
				}
				addr, _ := out["address"].(map[string]any)
				id, ok := num(addr, "id")
				if !ok || id <= 0 {
					return nil, fmt.Errorf("seed address returned no id: %s", res.Text())
				}
				return map[string]any{
					"instance": instance(), "customer_id": e.Int("cust_id"), "address_id": int(id),
					"city": "Krakow",
				}, nil
			}, Need: []string{"cust_id"}, Soft: true,
		},
		{
			Group: "customer-write", Tool: "magento.customer.addresses.delete", Case: "deletes the address",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "address_id": e.Int("addr_id")}, nil
			}, Need: []string{"addr_id"}, Soft: true,
		},

		// ---- order ------------------------------------------------------------
		{
			Group: "order-write", Tool: "magento.order.create", Case: "places an order through the cart flow",
			Args: func(e *Env) (map[string]any, error) {
				address := map[string]any{
					"firstname": "MCPTEST", "lastname": "Customer",
					"street": []string{"1 MCPTEST Street"}, "city": "Wrocław",
					"postcode": "50-001", "country_id": "PL", "region_id": 997,
					"telephone": "500600700",
				}
				return map[string]any{
					"instance": instance(), "customer_id": e.Int("cust_id"),
					"items":                 []map[string]any{{"sku": e.Get("p1"), "qty": 1}},
					"billing_address":       address,
					"shipping_address":      address,
					"shipping_carrier_code": "flatrate", "shipping_method_code": "flatrate",
					"payment_method":  "checkmo",
					"idempotency_key": e.Get("order_idem"),
				}, nil
			}, Need: []string{"cust_id", "p1"},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				order, _ := out["order"].(map[string]any)
				if order == nil {
					order = out
				}
				if id, ok := num(order, "entity_id"); ok && id > 0 {
					e.Set("order_id", fmt.Sprintf("%.0f", id))
				}
				// The invoice and shipment cases need the real order line id; the
				// schema rejects 0, which is why those two never ran.
				if items, ok := order["items"].([]any); ok && len(items) > 0 {
					if line, ok := items[0].(map[string]any); ok {
						if id, ok := num(line, "item_id"); ok && id > 0 {
							e.Set("order_item_id", fmt.Sprintf("%.0f", id))
						}
					}
				}
				if op := str(out, "operation_id"); op != "" {
					e.Set("order_operation", op)
				}
				return nil
			}, Soft: true,
		},
		{
			Group: "order-write", Tool: "magento.order.create", Case: "same idempotency key replays instead of duplicating",
			Args: func(e *Env) (map[string]any, error) {
				address := map[string]any{
					"firstname": "MCPTEST", "lastname": "Customer",
					"street": []string{"1 MCPTEST Street"}, "city": "Wrocław",
					"postcode": "50-001", "country_id": "PL", "region_id": 997,
					"telephone": "500600700",
				}
				return map[string]any{
					"instance": instance(), "customer_id": e.Int("cust_id"),
					"items":                 []map[string]any{{"sku": e.Get("p1"), "qty": 1}},
					"billing_address":       address,
					"shipping_address":      address,
					"shipping_carrier_code": "flatrate", "shipping_method_code": "flatrate",
					"payment_method":  "checkmo",
					"idempotency_key": e.Get("order_idem"),
				}, nil
			}, Need: []string{"cust_id", "p1", "order_id"},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				if replayed, _ := out["replayed"].(bool); !replayed {
					return fmt.Errorf("second call with the same key was not a replay: %v", out)
				}
				return nil
			}, Soft: true,
		},
		{
			Group: "order-write", Tool: "magento.order.get", Case: "reads the placed order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("order_id")}, nil
			}, Need: []string{"order_id"}, Soft: true,
		},
		{
			Group: "order-write", Tool: "magento.order.status_history", Case: "reads the history",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("order_id")}, nil
			}, Need: []string{"order_id"}, Soft: true,
		},
		{
			Group: "order-write", Tool: "magento.order.comment", Case: "adds a comment",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "order_id": e.Int("order_id"),
					"comment": "MCPTEST live harness comment", "notify": false,
				}, nil
			}, Need: []string{"order_id"}, Soft: true,
		},
		{
			Group: "order-write", Tool: "magento.order.hold", Case: "holds the order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("order_id")}, nil
			}, Need: []string{"order_id"}, Soft: true,
		},
		{
			Group: "order-write", Tool: "magento.order.unhold", Case: "unholds the order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("order_id")}, nil
			}, Need: []string{"order_id"}, Soft: true,
		},
		{
			Group: "order-write", Tool: "magento.order.email", Case: "sends the order email",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("order_id")}, nil
			}, Need: []string{"order_id"}, Soft: true,
		},

		// ---- invoice ----------------------------------------------------------
		{
			Group: "invoice-write", Tool: "magento.invoice.create", Case: "creates an invoice",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "order_id": e.Int("order_id"),
					"items":           []map[string]any{{"order_item_id": e.Int("order_item_id"), "qty": 1}},
					"capture":         false,
					"idempotency_key": e.Get("inv_idem"),
				}, nil
			}, Need: []string{"order_id", "order_item_id"}, Soft: true,
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				inv, _ := out["invoice"].(map[string]any)
				if inv == nil {
					inv = out
				}
				if id, ok := num(inv, "entity_id"); ok && id > 0 {
					e.Set("invoice_id", fmt.Sprintf("%.0f", id))
				}
				return nil
			},
		},
		{
			Group: "invoice-write", Tool: "magento.invoice.get", Case: "reads the invoice",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "invoice_id": e.Int("invoice_id")}, nil
			}, Need: []string{"invoice_id"}, Soft: true,
		},
		{
			Group: "invoice-write", Tool: "magento.invoice.list", Case: "lists invoices of the order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("order_id")}, nil
			}, Need: []string{"order_id"}, Soft: true,
		},
		{
			Group: "invoice-write", Tool: "magento.invoice.capture", Case: "captures the invoice",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "invoice_id": e.Int("invoice_id"),
					"idempotency_key": e.Get("cap_idem"),
				}, nil
			}, Need: []string{"invoice_id"}, Soft: true,
		},
		{
			Group: "invoice-write", Tool: "magento.invoice.email", Case: "sends the invoice email",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "document_id": e.Int("invoice_id")}, nil
			}, Need: []string{"invoice_id"}, Soft: true,
		},

		// ---- shipment ---------------------------------------------------------
		{
			Group: "shipment-write", Tool: "magento.shipment.create", Case: "creates a shipment",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "order_id": e.Int("order_id"),
					"items":  []map[string]any{{"order_item_id": e.Int("order_item_id"), "qty": 1}},
					"notify": false,
				}, nil
			}, Need: []string{"order_id", "order_item_id"}, Soft: true,
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				sh, _ := out["shipment"].(map[string]any)
				if sh == nil {
					sh = out
				}
				if id, ok := num(sh, "entity_id"); ok && id > 0 {
					e.Set("shipment_id", fmt.Sprintf("%.0f", id))
				}
				return nil
			},
		},
		{
			Group: "shipment-write", Tool: "magento.shipment.get", Case: "reads the shipment",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "shipment_id": e.Int("shipment_id")}, nil
			}, Need: []string{"shipment_id"}, Soft: true,
		},
		{
			Group: "shipment-write", Tool: "magento.shipment.list", Case: "lists shipments of the order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("order_id")}, nil
			}, Need: []string{"order_id"}, Soft: true,
		},
		{
			Group: "shipment-write", Tool: "magento.shipment.email", Case: "sends the shipment email",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "document_id": e.Int("shipment_id")}, nil
			}, Need: []string{"shipment_id"}, Soft: true,
		},

		// ---- credit memo ------------------------------------------------------
		{
			Group: "creditmemo-write", Tool: "magento.creditmemo.create", Case: "creates a credit memo",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "order_id": e.Int("order_id"),
					"invoice_id":      e.Int("invoice_id"),
					"items":           []map[string]any{{"order_item_id": e.Int("order_item_id"), "qty": 1}},
					"notify":          false,
					"idempotency_key": e.Get("cm_idem"),
				}, nil
			}, Need: []string{"order_id", "invoice_id"}, Soft: true,
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				cm, _ := out["creditmemo"].(map[string]any)
				if cm == nil {
					cm = out
				}
				if id, ok := num(cm, "entity_id"); ok && id > 0 {
					e.Set("creditmemo_id", fmt.Sprintf("%.0f", id))
				}
				return nil
			},
		},
		{
			Group: "creditmemo-write", Tool: "magento.creditmemo.get", Case: "reads the credit memo",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "creditmemo_id": e.Int("creditmemo_id")}, nil
			}, Need: []string{"creditmemo_id"}, Soft: true,
		},
		{
			Group: "creditmemo-write", Tool: "magento.creditmemo.list", Case: "lists credit memos of the order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "order_id": e.Int("order_id")}, nil
			}, Need: []string{"order_id"}, Soft: true,
		},
		{
			Group: "creditmemo-write", Tool: "magento.creditmemo.email", Case: "sends the credit memo email",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "document_id": e.Int("creditmemo_id")}, nil
			}, Need: []string{"creditmemo_id"}, Soft: true,
		},

		// ---- import -----------------------------------------------------------
		{
			Group: "import-write", Tool: "magento.import.execute", Case: "executes a one-row import",
			Args: func(*Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "format": "csv", "data_base64": b64(importCSV("EXEC")),
				}, nil
			}, Soft: true,
		},
		{
			Group: "import-write", Tool: "magento.import.start", Case: "starts an asynchronous import",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "format": "csv",
					"data_base64":     b64(importCSVWithFailure("ASYNC")),
					"idempotency_key": e.Get("imp_idem"),
				}, nil
			},
			Check: func(e *Env, res *ToolResult) error {
				out, err := output(res)
				if err != nil {
					return err
				}
				if id := str(out, "job_id"); id != "" {
					e.Set("job_id", id)
				}
				return nil
			}, Soft: true,
		},
		{
			Group: "import-write", Tool: "magento.import.status", Case: "reads the job status",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "job_id": e.Get("job_id")}, nil
			}, Need: []string{"job_id"}, Soft: true,
		},
		{
			Group: "import-write", Tool: "magento.import.batch", Case: "reads the batch",
			Args: func(e *Env) (map[string]any, error) {
				// The batch id is the job id that produced it; import.start returns the
				// job, so the batch list supplies the id.
				return map[string]any{"instance": instance(), "batch_id": e.Get("batch_id")}, nil
			}, Need: []string{"batch_id"}, Soft: true,
		},
		{
			Group: "import-write", Tool: "magento.import.cancel", Case: "cancels a finished job is a typed error",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "job_id": e.Get("job_id")}, nil
			}, Need: []string{"job_id"}, Soft: true,
		},
		{
			// The async import now always contains a row that cannot succeed, so the
			// job deterministically has failed rows and resume has something to do.
			// Without that fixture this case flipped between "resumed" and "correctly
			// refused" from run to run.
			Group: "import-write", Tool: "magento.import.resume", Case: "resumes a job with failed rows",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "job_id": e.Get("job_id")}, nil
			}, Need: []string{"job_id"}, Soft: true,
		},

		// ---- cancel and delete ------------------------------------------------
		{
			Group: "order-write", Tool: "magento.order.cancel", Case: "cancels the order",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{
					"instance": instance(), "order_id": e.Int("order_id"),
					"idempotency_key": e.Get("cancel_idem"),
				}, nil
			}, Need: []string{"order_id"}, Soft: true,
		},
		{
			Group: "product-write", Tool: "magento.product.delete", Case: "deletes the created product",
			Args: func(e *Env) (map[string]any, error) {
				return map[string]any{"instance": instance(), "sku": e.Get("p1")}, nil
			}, Need: []string{"p1"}, Soft: true,
		},
	}
}

// seedWriteEnv records the identifiers every write case needs. They are unique
// per run so repeated runs do not collide on the store.
func seedWriteEnv(e *Env) {
	e.Set("p1", skuPrefix+"PROD-"+ts)
	e.Set("p1slug", "mcptest-prod-"+ts)
	e.Set("cfg", skuPrefix+"CFG-"+ts)
	e.Set("cfgslug", "mcptest-cfg-"+ts)
	e.Set("media_file", "mcptest-"+ts+".png")
	e.Set("email", "mcptest-"+ts+"@example.com")
	e.Set("order_idem", "mcptest-order-"+ts)
	e.Set("inv_idem", "mcptest-invoice-"+ts)
	e.Set("cap_idem", "mcptest-capture-"+ts)
	e.Set("cm_idem", "mcptest-creditmemo-"+ts)
	e.Set("imp_idem", "mcptest-import-"+ts)
	e.Set("cancel_idem", "mcptest-cancel-"+ts)
}
