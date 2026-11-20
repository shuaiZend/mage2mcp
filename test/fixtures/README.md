# Recorded Magento payloads

`magento/` holds responses captured from a real Magento 2 store
(`https://demo-magento-2.auroracreation.com`, store view `pl`) on 2026-11-21.
`internal/magento/conformance_test.go` replays them unchanged through the client,
so the shapes a real store returns stay under regression without an environment.

These are fixtures, not integration tests: they cannot notice a store changing its
behaviour, because they are frozen. What they do catch is the gateway drifting away
from a payload it once parsed correctly — which is what every fake in the suite
cannot do, since we write the fakes ourselves.

Re-capture with:

```bash
./test/fixtures/capture-magento.sh test/fixtures/magento
```

The store is public demo data; no credentials are stored here. Re-capturing needs
the demo admin account, so treat the script as documentation of how the fixtures
were produced rather than something to run in CI.
