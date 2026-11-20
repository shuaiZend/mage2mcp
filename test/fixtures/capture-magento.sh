#!/bin/sh
# Capture real Magento responses for the conformance fixtures.
set -eu
BASE="https://demo-magento-2.auroracreation.com/rest"
STORE="pl"
OUT="$1"
TOKEN=$(curl -sS --max-time 25 -X POST "$BASE/V1/integration/admin/token" \
  -H 'Content-Type: application/json' \
  -d '{"username":"demo_admin","password":"demo_admin123"}' | tr -d '"')
[ -n "$TOKEN" ] || { echo "no token" >&2; exit 1; }
get() { # name path
  curl -sS --max-time 30 -H "Authorization: Bearer $TOKEN" "$BASE/$STORE/V1/$2" -o "$OUT/$1.json" -w "$1 %{http_code}\n"
}
get products            'products?searchCriteria%5BpageSize%5D=2'
get product_one         'products/WJ01'
get orders              'orders?searchCriteria%5BpageSize%5D=2&searchCriteria%5BsortOrders%5D%5B0%5D%5Bfield%5D=entity_id&searchCriteria%5BsortOrders%5D%5B0%5D%5Bdirection%5D=desc'
get inventory_sources   'inventory/sources?searchCriteria%5BpageSize%5D=5'
get inventory_stocks    'inventory/stocks?searchCriteria%5BpageSize%5D=5'
get store_configs       'store/storeConfigs'
get categories          'categories?searchCriteria%5BpageSize%5D=3'
get customer_groups     'customerGroups/search?searchCriteria%5BpageSize%5D=5'
get tax_classes         'taxClasses'
get media_types         'products/media/types/Default'
get attribute_sets      'products/attribute-sets/sets/list?searchCriteria%5BpageSize%5D=5'
get source_items        'inventory/source-items?searchCriteria%5BpageSize%5D=2'
