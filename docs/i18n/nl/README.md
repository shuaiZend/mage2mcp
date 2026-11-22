<!-- translated-from: README.md @ 0326e3c -->

# mage2mcp — Nederlandse introductie

Een MCP-server voor Magento 2. Een AI-agent praat er MCP mee; hij praat REST met Magento, en de
agent heeft nooit een Magento-inloggegeven in handen.

Andere talen: [English](../../../README.md) · [中文](../zh/README.md) · [Español](../es/README.md) ·
Volledige handleiding: [Nederlands](tutorial.md).

## Wat het biedt

- **105 tools**: catalogus en producten, bestellingen en geld (facturen, zendingen,
  creditnota's), voorraad en MSI, klanten en adressen, imports, rapporten, en uitlezen van
  Cloudflare en GA4
- **Toegang standaard geweigerd**: `read < write < destructive < admin`, gebonden aan de instantie
- **Goedkeuring door een mens in twee stappen**: elke schrijfactie wordt door **een ander mens**
  goedgekeurd, gebonden aan de tool, de instantie en een hash van de argumenten
- **Operatieboek**: elke schrijfactie heeft een `intent` en een `outcome`; een onduidelijk
  resultaat wordt `operation_unknown`, met tools om het te bekijken en af te handelen
- **Idempotentie**: opnieuw proberen met een `idempotency_key` speelt het resultaat opnieuw af in
  plaats van het te herhalen

## Snelstart

```bash
go build -o bin/mage2mcp ./cmd/server
go build -o bin/mage2mcp-admin ./cmd/admin

cp configs/config.example.yaml config.yaml
./bin/mage2mcp -config config.yaml

curl -s localhost:8080/readyz   # {"status":"ready","tools":1}
```

`tools: 1` klopt: het meegeleverde voorbeeld zet geen databron aan en stelt alleen
`platform.info` beschikbaar. Met een Magento-databron worden dat er 105.

Het MCP-eindpunt is `POST /mcp` over Streamable HTTP:

```json
{
  "mcpServers": {
    "mage2mcp": {
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

## Drie dingen die je moet weten

1. **Schrijftools staan standaard uit.** Je hebt `tools.writes: true` nodig, en elke schrijfactie
   wordt door een ander mens goedgekeurd via de beheer-CLI. Goedkeuring vereist `database.url`
   (PostgreSQL).
2. **`instance` is verplicht.** Elke Magento-tool moet een geconfigureerde instantie benoemen; een
   typefout geeft een expliciete fout in plaats van stil een andere winkel te lezen.
3. **Probeer niet opnieuw als het resultaat onduidelijk is.** `operation_unknown` betekent dat het
   stroomopwaarts uitgevoerd kan zijn. Gebruik `magento.operation.get` om het te bekijken en
   `magento.operation.dispose` om een menselijk oordeel vast te leggen.

## Waar je verder leest

| Doel | Document |
|---|---|
| Volledige handleiding | [Nederlandse handleiding](tutorial.md) |
| Beschrijving per tool | [mcp-tools.md](../../mcp-tools.md) |
| Configuratiereferentie | [configuration.md](../../configuration.md) |
| Uitrollen in productie | [operations/deployment.md](../../operations/deployment.md) |
| Bekende beperkingen | [audit/known-limitations.md](../../audit/known-limitations.md) |

## Veiligheidswaarschuwing

De meegeleverde ontwikkelconfiguratie heeft **authenticatie uit**, gebruikt vaste
plaatsvervangende wachtwoorden en luistert alleen op loopback. Stel die **nooit** bloot aan een
netwerk. Begin voor productie bij `configs/config.production.template.yaml` en lever
`MAGE2MCP_AUTH_*` en `MAGE2MCP_DATABASE_URL` aan.
