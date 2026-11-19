# End-to-end test

`run.sh` exercises the gateway against a fake Magento REST API:

1. starts PostgreSQL from `docker-compose.yml` with a temporary port override;
2. builds and starts the fake Magento API, the worker, and the server on
   loopback with writes enabled and authentication disabled (test only);
3. calls the MCP tools: `tools/list`, `magento.product.create` through the
   two-step confirmation (approved by a different identity), `magento.product.seo`,
   `magento.import.validate`, and an asynchronous `magento.import.start` that the
   worker executes;
4. asserts the import report contains `created: 1`, then cleans up.

```bash
make e2e          # or: ./test/e2e/run.sh
```

Environment overrides: `E2E_PG_PORT`, `E2E_API_PORT`, `E2E_MAGENTO_PORT`,
`E2E_PG_PASSWORD`.

This is a smoke test, not a substitute for testing against a real Magento
instance. It needs Docker and Go and is not part of the required CI gates.
