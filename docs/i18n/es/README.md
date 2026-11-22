<!-- translated-from: README.md @ 0326e3c -->

# mage2mcp — introducción en español

Un servidor MCP para Magento 2. Un agente de IA habla con él en MCP; él habla REST con Magento,
y el agente nunca posee una credencial de Magento.

Otros idiomas: [English](../../../README.md) · [中文](../zh/README.md) · [Nederlands](../nl/README.md) ·
Tutorial completo: [Español](tutorial.md).

## Qué ofrece

- **105 herramientas**: catálogo y productos, pedidos y dinero (facturas, envíos, notas de
  crédito), inventario y MSI, clientes y direcciones, importaciones, informes, y lecturas de
  Cloudflare y GA4
- **Autorización denegada por defecto**: `read < write < destructive < admin`, ligada a la
  instancia
- **Aprobación humana en dos pasos**: cada escritura la aprueba **otra persona**, ligada a la
  herramienta, la instancia y un hash de los argumentos
- **Libro de operaciones**: cada escritura tiene `intent` y `outcome`; un resultado ambiguo se
  registra como `operation_unknown` con herramientas para consultarlo y resolverlo
- **Idempotencia**: un reintento con `idempotency_key` reproduce el resultado en lugar de repetir

## Inicio rápido

```bash
go build -o bin/mage2mcp ./cmd/server
go build -o bin/mage2mcp-admin ./cmd/admin

cp configs/config.example.yaml config.yaml
./bin/mage2mcp -config config.yaml

curl -s localhost:8080/readyz   # {"status":"ready","tools":1}
```

`tools: 1` es correcto: el ejemplo incluido no activa ninguna fuente de datos y solo expone
`platform.info`. Con una fuente Magento activada pasan a ser 105.

El punto de entrada MCP es `POST /mcp` sobre Streamable HTTP:

```json
{
  "mcpServers": {
    "mage2mcp": {
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

## Tres cosas que hay que saber

1. **Las herramientas de escritura están apagadas por defecto.** Hace falta `tools.writes: true`,
   y cada escritura la aprueba otra persona mediante la CLI de administración. La aprobación
   requiere `database.url` (PostgreSQL).
2. **`instance` es obligatorio.** Cada herramienta de Magento debe nombrar una instancia
   configurada; una errata da un error explícito en lugar de leer en silencio otra tienda.
3. **No reintentes cuando el resultado es ambiguo.** `operation_unknown` significa que aguas
   arriba pudo haberse ejecutado. Usa `magento.operation.get` para consultarlo y
   `magento.operation.dispose` para registrar un juicio humano.

## Por dónde seguir

| Objetivo | Documento |
|---|---|
| Tutorial completo | [Tutorial en español](tutorial.md) |
| Descripción de cada herramienta | [mcp-tools.md](../../mcp-tools.md) |
| Referencia de configuración | [configuration.md](../../configuration.md) |
| Despliegue en producción | [operations/deployment.md](../../operations/deployment.md) |
| Limitaciones conocidas | [audit/known-limitations.md](../../audit/known-limitations.md) |

## Aviso de seguridad

La configuración de desarrollo incluida tiene la **autenticación desactivada**, contraseñas de
marcador de posición y solo escucha en loopback. **Nunca** la expongas a una red. Para producción
empieza por `configs/config.production.template.yaml` y aporta `MAGE2MCP_AUTH_*` y
`MAGE2MCP_DATABASE_URL`.
