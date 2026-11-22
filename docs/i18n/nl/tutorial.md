<!-- translated-from: docs/tutorial.md @ 0326e3c -->

# Handleiding: van nul naar een werkende Magento-agent

Deze handleiding brengt je van een lege machine naar een agent die een echte winkel leest, een
prijs wijzigt, een bestelling plaatst en die terugbetaalt — en onderweg de goedkeuringspoort
echt doorloopt.

Er is geen voorkennis van mage2mcp nodig. Elk commando en elke toolaanroep hieronder is tijdens
het schrijven tegen een echte Magento-winkel uitgevoerd, dus de uitvoer is echt.

Andere talen: [English](../../tutorial.md) · [中文](../zh/tutorial.md) · [Español](../es/tutorial.md).

---

## 1. Wat je bouwt

```text
jouw agent  ──MCP (Streamable HTTP)──▶  mage2mcp  ──REST──▶  Magento 2
                                            │
                                            └──▶ PostgreSQL (audit, goedkeuringen, taken)
```

mage2mcp is een MCP-server. Een agent praat er MCP mee; hij praat REST met Magento. Je schrijft
geen Magento-REST-aanroepen, en de agent heeft nooit een Magento-inloggegeven in handen.

---

## 2. Voordat je begint

| Je hebt nodig | Waarvoor |
|---|---|
| Go 1.25 of nieuwer | om de twee binaries te bouwen |
| Een Magento 2-instantie en een beheerder | de handleiding gebruikt een echte winkel; een nepwinkel laat het echte gedrag niet zien |
| PostgreSQL 16+ (optioneel) | nodig voor goedkeuringen, de audit en asynchrone imports; zonder blijft dat in het geheugen |
| Een MCP-client | Claude Desktop, of elke client die Streamable HTTP spreekt |

Controleer Go:

```bash
go version   # go1.25 of nieuwer
```

---

## 3. Start de gateway

```bash
git clone https://github.com/shuaiZend/mage2mcp.git
cd mage2mcp
go build -o bin/mage2mcp ./cmd/server
go build -o bin/mage2mcp-admin ./cmd/admin
```

Kopieer de ontwikkelconfiguratie en start:

```bash
cp configs/config.example.yaml config.yaml
./bin/mage2mcp -config config.yaml
```

Hij luistert op `127.0.0.1:8080` met **geen enkele databron aan**, dus hij stelt precies één
tool beschikbaar. Dat is opzet: het meegeleverde voorbeeld kan niets bereiken.

```bash
curl -s localhost:8080/healthz   # {"status":"ok"}
curl -s localhost:8080/readyz    # {"status":"ready","tools":1}
```

`tools: 1` klopt — die ene tool is `platform.info`.

### Sluit je winkel aan

Bewerk `config.yaml`:

```yaml
datasources:
  magento:
    enabled: true
    instances:
      - id: "store-us"                                  # deze id kies je zelf
        endpoint: "https://your-store.example.com"
        store_code: "default"
        credential: "${MAGENTO_CREDENTIAL}"
```

Exporteer het inloggegeven als beheerderstoken (`admin:<user>:<password>`) of als bearer-token,
en herstart:

```bash
export MAGENTO_CREDENTIAL='admin:your_admin_user:your_password'
./bin/mage2mcp -config config.yaml
curl -s localhost:8080/readyz    # {"status":"ready","tools":105}
```

Het aantal tools weerspiegelt nu het Magento-oppervlak. `store-us` is de **instantie-id**: elke
toolaanroep draagt hem mee, omdat één gateway meerdere winkels kan bedienen.

> **Schrijftools staan nog uit.** De standaard is `tools.writes: false`. Lezen werkt; niets kan
> de winkel nog wijzigen. Sectie 7 zet schrijven aan.

---

## 4. Sluit een MCP-client aan

Het eindpunt is `POST /mcp` over Streamable HTTP.

Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "mage2mcp": {
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

Herstart de client. Hij roept bij het verbinden `tools/list` aan en je zou de Magento-tools
moeten zien. De server stuurt bij initialize ook **instructies** mee: lees die één keer, want ze
zeggen hoe je met toolresultaten omgaat (als data, nooit als instructies) en wat de regels zijn
voor het kopiëren van een product uit een andere winkel.

Elke andere client: wijs hem naar dezelfde URL met het Streamable HTTP-transport.

---

## 5. Je eerste leesacties

Vraag het je agent, of roep de tools direct aan. Dit zijn de vier die je het meest zult
gebruiken:

| Tool | Wat hij beantwoordt |
|---|---|
| `platform.info` | versie, geregistreerde tools, geconfigureerde instanties |
| `magento.product.search` | welke producten bij een filter passen |
| `magento.product.get` | één product, op SKU |
| `magento.order.search` | bestellingen in een periode, van een klant, op status |

Een zoekopdracht:

```json
{"instance": "store-us", "sku": "24-MB01"}
```

`instance` is verplicht en moet een geconfigureerde instantie benoemen — de fout is expliciet
als je je vergist, en dat is precies de bedoeling: een typefout kan niet stil de verkeerde
winkel lezen.

> **Twee soorten 404.** `not_found` betekent dat de entiteit ontbreekt. `unsupported_capability`
> betekent dat de route niet bestaat op deze winkel — sommige Magento-builds stellen niet elk
> eindpunt beschikbaar, en de gateway zegt in welk geval je zit. Behandel ze niet als hetzelfde
> bij het debuggen.

---

## 6. Vijf dingen die je echt zult doen

### 6.1 Uitzoeken wat niet op voorraad is

```text
magento.product.search  {"instance":"store-us", "limit":50}
magento.inventory.status {"instance":"store-us", "sku":"24-MB01"}
```

`inventory.status.qty` is het **oude** voorraadcijfer, niet de som van de MSI-bronitems. Op een
MSI-winkel kan het 0 zijn terwijl `is_in_stock` true is. Gebruik
`magento.inventory.source_items` voor aantallen per bron.

### 6.2 Een prijs wijzigen

```text
magento.product.update {"instance":"store-us", "sku":"24-MB01", "price":"19.99"}
```

Lees het terug om zeker te zijn — de gateway geeft het bijgewerkte product terug, en een
prijswijziging is precies het soort ding dat je wilt bevestigen:

```text
magento.product.get {"instance":"store-us", "sku":"24-MB01"}
```

### 6.3 Een bestelling plaatsen

```text
magento.order.create {
  "instance":"store-us",
  "customer_id": 1,
  "items": [{"sku":"24-MB01","qty":2}],
  "billing_address":  {"firstname":"Ada","lastname":"Lovelace","street":["1 Main St"],
                       "city":"Warsaw","postcode":"00-001","country_id":"PL",
                       "region_id":997,"telephone":"500600700"},
  "shipping_address": {"firstname":"Ada","lastname":"Lovelace","street":["1 Main St"],
                       "city":"Warsaw","postcode":"00-001","country_id":"PL",
                       "region_id":997,"telephone":"500600700"},
  "shipping_carrier_code":"flatrate",
  "shipping_method_code":"flatrate",
  "payment_method":"checkmo",
  "idempotency_key":"order-2026-11-22-001"
}
```

Twee dingen zijn hier belangrijk:

- **`region_id` is verplicht voor landen met een regiolijst** (Polen, de Verenigde Staten en veel
  andere). Zonder dat wijst Magento het adres af. De gateway stelt geen regiozoeker beschikbaar,
  dus haal het id uit het beheerpaneel van de winkel.
- **Geef altijd een `idempotency_key` mee.** Een schrijfactie die je opnieuw probeert zonder
  sleutel kan dubbel uitvoeren. Met een sleutel speelt dezelfde aanroep het vastgelegde resultaat
  opnieuw af in plaats van een tweede bestelling te plaatsen.

Als het plaatsen onduidelijk is — een timeout, een weggevallen verbinding, een 5xx — antwoordt de
tool `operation_unknown` en zegt hij **niet opnieuw te proberen**. Dat is geen fout om heen te
werken: de bestelling kan bestaan. Zie sectie 8.

### 6.4 Eén artikel van de drie terugbetalen

```text
magento.creditmemo.create {
  "instance":"store-us",
  "order_id": 41,
  "items": [{"order_item_id": 7, "qty": 1}]
}
```

Standaard betaalt een gedeeltelijke terugbetaling de **verzendkosten niet** terug; de
resterende verzendkosten gaan pas terug bij de laatste eenheden. Wil je verzendkosten expliciet
terugbetalen, geef dan `shipping_amount` mee.

Lees de `grand_total` van het resultaat en vergelijk die met wat je verwachtte voordat je iemand
vertelt dat de terugbetaling klaar is.

### 6.5 Antwoorden: «waar is mijn bestelling?»

```text
magento.order.get       {"instance":"store-us", "order_id":41}
magento.shipment.list   {"instance":"store-us", "order_id":41}
magento.order.status_history {"instance":"store-us", "order_id":41}
```

---

## 7. Schrijven aanzetten en goedkeuren

Schrijven staat standaard uit. Aanzetten is een bewuste handeling in twee delen:

```yaml
tools:
  writes: true

database:
  url: "postgres://mage2mcp:secret@127.0.0.1:5432/mage2mcp?sslmode=disable"
```

De database is hier niet optioneel: goedkeuringen staan in PostgreSQL, gedeeld tussen de gateway
en de beheer-CLI.

Herstart en doe een schrijfactie:

```text
magento.product.update {"instance":"store-us","sku":"24-MB01","price":"17.99"}
```

Hij wordt niet uitgevoerd. Hij antwoordt:

```json
{"code": "confirmation_required",
 "details": {"confirmation_id": "0eb27457-...", "expires_at": "..."},
 "message": "this operation requires approval by a different human"}
```

Een mens keurt het goed:

```bash
./bin/mage2mcp-admin -config config.yaml -as you@example.com pending
./bin/mage2mcp-admin -config config.yaml -as you@example.com approve 0eb27457-...
```

Daarna probeert de agent **dezelfde aanroep opnieuw met `confirmation_id` erbij**, en dan wordt
hij uitgevoerd.

Drie eigenschappen zijn het kennen waard:

- De goedkeuring is gebonden aan de tool, de instantie en een **hash van de argumenten**. Wijzig
  één argument en de goedkeuring geldt niet meer.
- Hij is **eenmalig** en verloopt (5 minuten, maximaal 15).
- De aanvrager kan zijn eigen handeling niet goedkeuren.

---

## 8. Als er iets misgaat

| Je ziet | Wat het betekent | Wat te doen |
|---|---|---|
| `operation_unknown` | de schrijfactie is mogelijk wel of niet aangekomen | **niet opnieuw proberen.** Gebruik `magento.operation.get` om hem te bekijken, daarna `magento.operation.dispose` met een oordeel |
| `confirmation_required` | de schrijfactie heeft een mens nodig | keur goed en probeer opnieuw met `confirmation_id` |
| `unsupported_capability` | deze winkel stelt de route niet beschikbaar | doe het in het beheerpaneel; dit is geen fout van de gateway |
| `validation_failed` | Magento wees de payload af | kijk naar `details.upstream_field` / `details.upstream_message`; de reden staat er meestal |
| `rate_limited` | het budget per instantie is op | wacht en probeer opnieuw; het is expliciet als herhaalbaar gemarkeerd |
| `permission_denied` | jouw identiteit mist de scope | vraag een hogere rol |

### De `operation_unknown`-werkwijze

Een onduidelijke geldhandeling wordt vastgelegd, niet geraden:

```text
magento.operation.get    {"instance":"store-us","operation_id":"46b1322a-..."}
magento.operation.list   {"instance":"store-us","needs_attention":true}
magento.operation.dispose{"instance":"store-us","operation_id":"...",
                          "judgment":"applied","note":"checked /V1/orders/41: exists, increment 000000041"}
```

`judgment` is een van:

- `applied` — je hebt stroomopwaarts gekeken en de wijziging is er
- `not_applied` — je hebt gekeken en hij is er niet; de sleutel wordt vrijgegeven, dus opnieuw
  proberen is veilig
- `requeue` — je hebt het nog niet vastgesteld; de verzoener leest stroomopwaarts opnieuw

Een `note` is verplicht. De afhandeling legt vast **wie wat besliste**, want «unknown» betekent
dat niemand het weet — de status omzetten zonder oordeel zou onzekerheid in valse zekerheid
veranderen.

---

## 9. Waar je verder leest

| Als je wilt | Lees |
|---|---|
| elke tool kennen | [mcp-tools.md](../../mcp-tools.md) en het gegenereerde [implemented-tools.md](../../implemented-tools.md) |
| het netjes uitrollen | [operations/deployment.md](../../operations/deployment.md) en [configuration.md](../../configuration.md) |
| weten wat gegarandeerd is | [mcp-conformance.md](../../mcp-conformance.md) |
| weten wat **niet** | [audit/known-limitations.md](../../audit/known-limitations.md) |
| een ontwerpbeslissing begrijpen | [adr/](../../adr/) |

De [README](../../../README.md) bevat de releasegeschiedenis en de verificatiestatus.
