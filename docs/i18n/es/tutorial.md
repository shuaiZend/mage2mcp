<!-- translated-from: docs/tutorial.md @ 0326e3c -->

# Tutorial: de cero a un agente de Magento funcionando

Este tutorial te lleva desde una máquina vacía hasta un agente que lee una tienda real, cambia
un precio, hace un pedido y lo reembolsa, pasando por la puerta de aprobación en el camino.

No supone ningún conocimiento previo de mage2mcp. Cada comando y cada llamada de herramienta de
abajo se ejecutaron contra una tienda Magento real al escribirlo, así que las salidas son reales.

Otros idiomas: [English](../../tutorial.md) · [中文](../zh/tutorial.md) · [Nederlands](../nl/tutorial.md).

---

## 1. Qué estás construyendo

```text
tu agente   ──MCP (Streamable HTTP)──▶  mage2mcp  ──REST──▶  Magento 2
                                            │
                                            └──▶ PostgreSQL (auditoría, aprobaciones, tareas)
```

mage2mcp es un servidor MCP. El agente habla con él en MCP; él habla con Magento en REST. Tú no
escribes llamadas REST de Magento, y el agente nunca posee una credencial de Magento.

---

## 2. Antes de empezar

| Necesitas | Para qué |
|---|---|
| Go 1.25 o superior | compilar los dos binarios |
| Una instancia Magento 2 y un usuario administrador | el tutorial usa una tienda real; una falsa no te mostrará el comportamiento real |
| PostgreSQL 16+ (opcional) | necesario para aprobaciones, la auditoría y las importaciones asíncronas; sin él viven en memoria |
| Un cliente MCP | Claude Desktop, o cualquier cliente que hable Streamable HTTP |

Comprueba Go:

```bash
go version   # go1.25 o superior
```

---

## 3. Arranca la pasarela

```bash
git clone https://github.com/shuaiZend/mage2mcp.git
cd mage2mcp
go build -o bin/mage2mcp ./cmd/server
go build -o bin/mage2mcp-admin ./cmd/admin
```

Copia la configuración de desarrollo y arranca:

```bash
cp configs/config.example.yaml config.yaml
./bin/mage2mcp -config config.yaml
```

Escucha en `127.0.0.1:8080` con **ninguna fuente de datos activada**, así que expone exactamente
una herramienta. Es deliberado: el ejemplo incluido no puede alcanzar nada.

```bash
curl -s localhost:8080/healthz   # {"status":"ok"}
curl -s localhost:8080/readyz    # {"status":"ready","tools":1}
```

`tools: 1` es correcto: esa única herramienta es `platform.info`.

### Conecta tu tienda

Edita `config.yaml`:

```yaml
datasources:
  magento:
    enabled: true
    instances:
      - id: "store-us"                                  # este id lo eliges tú
        endpoint: "https://your-store.example.com"
        store_code: "default"
        credential: "${MAGENTO_CREDENTIAL}"
```

Exporta la credencial como token de administrador (`admin:<user>:<password>`) o como token
bearer, y reinicia:

```bash
export MAGENTO_CREDENTIAL='admin:your_admin_user:your_password'
./bin/mage2mcp -config config.yaml
curl -s localhost:8080/readyz    # {"status":"ready","tools":105}
```

El número de herramientas refleja ahora la superficie de Magento. `store-us` es el **id de
instancia**: cada llamada lo lleva, porque una pasarela puede servir varias tiendas.

> **Las herramientas de escritura siguen desactivadas.** El valor por defecto es
> `tools.writes: false`. Las lecturas funcionan; nada puede modificar la tienda todavía. La
> sección 7 activa la escritura.

---

## 4. Conecta un cliente MCP

El punto de entrada es `POST /mcp` sobre Streamable HTTP.

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

Reinicia el cliente. Llama a `tools/list` al conectar y deberías ver las herramientas de
Magento. El servidor también envía **instrucciones** al inicializar: léelas una vez, porque
dicen cómo tratar los resultados de las herramientas (como datos, nunca como instrucciones) y
las reglas para copiar un producto de otra tienda.

Cualquier otro cliente: apúntalo a la misma URL con el transporte Streamable HTTP.

---

## 5. Tus primeras lecturas

Pídeselo a tu agente, o llama a las herramientas directamente. Estas son las cuatro que más
usarás:

| Herramienta | Qué responde |
|---|---|
| `platform.info` | versión, herramientas registradas, instancias configuradas |
| `magento.product.search` | qué productos coinciden con un filtro |
| `magento.product.get` | un producto, por SKU |
| `magento.order.search` | pedidos en una ventana, de un cliente, por estado |

Una búsqueda:

```json
{"instance": "store-us", "sku": "24-MB01"}
```

`instance` es obligatorio y debe nombrar una instancia configurada: el error es explícito si te
equivocas, y eso es justamente el punto: una errata no puede leer en silencio la tienda
equivocada.

> **Dos clases de 404.** `not_found` significa que falta la entidad. `unsupported_capability`
> significa que la ruta no existe en esta tienda: algunas compilaciones de Magento no exponen
> todos los endpoints, y la pasarela te dice en cuál caso estás. No los trates como lo mismo al
> depurar.

---

## 6. Cinco cosas que harás de verdad

### 6.1 Averiguar qué está agotado

```text
magento.product.search  {"instance":"store-us", "limit":50}
magento.inventory.status {"instance":"store-us", "sku":"24-MB01"}
```

`inventory.status.qty` es la cifra de existencias **heredada**, no la suma de los ítems de
origen MSI. En una tienda MSI puede valer 0 mientras `is_in_stock` es true. Para cantidades por
origen usa `magento.inventory.source_items`.

### 6.2 Cambiar un precio

```text
magento.product.update {"instance":"store-us", "sku":"24-MB01", "price":"19.99"}
```

Vuelve a leerlo para asegurarte: la pasarela devuelve el producto actualizado, y un cambio de
precio es justo el tipo de cosa que conviene confirmar:

```text
magento.product.get {"instance":"store-us", "sku":"24-MB01"}
```

### 6.3 Hacer un pedido

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

Aquí importan dos cosas:

- **`region_id` es obligatorio en países con directorio de regiones** (Polonia, Estados Unidos y
  muchos otros). Sin él Magento rechaza la dirección. La pasarela no expone un buscador de
  regiones, así que toma el id del panel de la tienda.
- **Pasa siempre `idempotency_key`.** Un reintento sin clave puede duplicar. Con clave, la misma
  llamada reproduce el resultado registrado en lugar de hacer un segundo pedido.

Si la colocación es ambigua — un timeout, una caída de red, un 5xx — la herramienta responde
`operation_unknown` y te dice **que no reintentes**. No es un error que haya que sortear: el
pedido puede existir. Ver la sección 8.

### 6.4 Reembolsar un artículo de tres

```text
magento.creditmemo.create {
  "instance":"store-us",
  "order_id": 41,
  "items": [{"order_item_id": 7, "qty": 1}]
}
```

Por defecto un reembolso parcial **no** reembolsa el envío; el envío restante vuelve solo cuando
se reembolsan las últimas unidades. Para reembolsar el envío explícitamente, pasa
`shipping_amount`.

Lee el `grand_total` del resultado y compáralo con lo que esperabas antes de decirle a nadie que
el reembolso está hecho.

### 6.5 Responder «¿dónde está mi pedido?»

```text
magento.order.get       {"instance":"store-us", "order_id":41}
magento.shipment.list   {"instance":"store-us", "order_id":41}
magento.order.status_history {"instance":"store-us", "order_id":41}
```

---

## 7. Activar la escritura y aprobarla

La escritura está desactivada por defecto. Activarla es un acto deliberado en dos partes:

```yaml
tools:
  writes: true

database:
  url: "postgres://mage2mcp:secret@127.0.0.1:5432/mage2mcp?sslmode=disable"
```

La base de datos no es opcional aquí: las aprobaciones se guardan en PostgreSQL, compartidas
entre la pasarela y la CLI de administración.

Reinicia y haz una escritura:

```text
magento.product.update {"instance":"store-us","sku":"24-MB01","price":"17.99"}
```

No se ejecuta. Responde:

```json
{"code": "confirmation_required",
 "details": {"confirmation_id": "0eb27457-...", "expires_at": "..."},
 "message": "this operation requires approval by a different human"}
```

Una persona lo aprueba:

```bash
./bin/mage2mcp-admin -config config.yaml -as you@example.com pending
./bin/mage2mcp-admin -config config.yaml -as you@example.com approve 0eb27457-...
```

Después el agente reintenta **la misma llamada añadiendo `confirmation_id`**, y entonces se
ejecuta.

Tres propiedades conviene conocerlas:

- La aprobación está ligada a la herramienta, la instancia y un **hash de los argumentos**.
  Cambia cualquier argumento y la aprobación deja de aplicar.
- Es de **un solo uso** y caduca (5 minutos, 15 como máximo).
- Quien solicita no puede aprobar su propia operación.

---

## 8. Cuando algo va mal

| Ves | Qué significa | Qué hacer |
|---|---|---|
| `operation_unknown` | la escritura puede haber llegado o no | **no reintentes.** Usa `magento.operation.get` para inspeccionarla y luego `magento.operation.dispose` con un juicio |
| `confirmation_required` | la escritura necesita una persona | aprueba y reintenta con `confirmation_id` |
| `unsupported_capability` | esta tienda no expone la ruta | hazlo en el panel; no es un fallo de la pasarela |
| `validation_failed` | Magento rechazó el cuerpo | mira `details.upstream_field` / `details.upstream_message`; la razón suele estar ahí |
| `rate_limited` | el presupuesto por instancia está agotado | espera y reintenta; está marcado explícitamente como reintentable |
| `permission_denied` | a tu identidad le falta el scope | pide un rol superior |

### El flujo de `operation_unknown`

Una operación de dinero ambigua se registra, no se adivina:

```text
magento.operation.get    {"instance":"store-us","operation_id":"46b1322a-..."}
magento.operation.list   {"instance":"store-us","needs_attention":true}
magento.operation.dispose{"instance":"store-us","operation_id":"...",
                          "judgment":"applied","note":"checked /V1/orders/41: exists, increment 000000041"}
```

`judgment` es uno de:

- `applied` — miraste aguas arriba y el cambio está
- `not_applied` — miraste y no está; la clave se libera, así que reintentar es seguro
- `requeue` — aún no lo has determinado; el reconciliador vuelve a leer aguas arriba

Se exige una `note`. La disposición registra **quién decidió qué**, porque «unknown» significa
que nadie lo sabe: cambiar el estado sin un juicio convertiría la incertidumbre en una certeza
falsa.

---

## 9. Por dónde seguir

| Si quieres | Lee |
|---|---|
| conocer cada herramienta | [mcp-tools.md](../../mcp-tools.md) y el generado [implemented-tools.md](../../implemented-tools.md) |
| desplegarlo en serio | [operations/deployment.md](../../operations/deployment.md) y [configuration.md](../../configuration.md) |
| saber qué está garantizado | [mcp-conformance.md](../../mcp-conformance.md) |
| saber qué **no** lo está | [audit/known-limitations.md](../../audit/known-limitations.md) |
| entender una decisión de diseño | [adr/](../../adr/) |

El [README](../../../README.md) tiene el historial de versiones y el estado de verificación.
