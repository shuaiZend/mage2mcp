#!/usr/bin/env node
// Design-contract checks for the Phase 0 documentation set.
//
// These are not style checks. They enforce invariants that the review process
// found easy to break by accident: tool names, tool scopes, the absence of a
// self-approval flag, the absence of universal execution tools, and contiguous
// ADR numbering. A failure here means the design documents contradict
// themselves, not that a line is too long.

import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

const root = process.cwd();
const failures = [];
const fail = (message) => failures.push(message);

const TOOL_RE = /^\| `((?:magento|cloudflare|ga4|unified|platform)\.[a-z0-9_.]+)` \| ([a-z]+) \|/gm;
const NAME_RE = /^(platform\.[a-z][a-z0-9_]*|(magento|cloudflare|ga4|unified)\.[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+)$/;
const SCOPES = new Set(["read", "write", "destructive", "admin"]);
const FORBIDDEN_TOOLS = ["execute_api", "execute_sql", "raw_graphql", "run_sql"];
const MCP_NAME_RE = /^[A-Za-z0-9_.-]{1,128}$/;

const toolsDoc = readFileSync(join(root, "docs/mcp-tools.md"), "utf8");
// The tool table lives in the generated inventory, not in the design page: mcp-tools.md
// now points at implemented-tools.md instead of duplicating a table that drifts. The
// generated file comes from the registry, so checking it validates what ships.
const inventoryDoc = readFileSync(join(root, "docs/implemented-tools.md"), "utf8");
const seen = new Map();
let toolCount = 0;

for (const match of inventoryDoc.matchAll(TOOL_RE)) {
  const [, name, scope] = match;
  toolCount += 1;
  if (!NAME_RE.test(name)) fail(`tool name does not match the naming grammar: ${name}`);
  if (!MCP_NAME_RE.test(name)) fail(`tool name violates the MCP character/length rule: ${name}`);
  if (!SCOPES.has(scope)) fail(`tool ${name} has unknown scope "${scope}"`);
  if (seen.has(name)) fail(`duplicate tool name: ${name}`);
  seen.set(name, scope);
}

if (toolCount === 0) fail("no tools found in docs/implemented-tools.md; the table format changed");

for (const forbidden of FORBIDDEN_TOOLS) {
  const defined = [...seen.keys()].some((name) => name.endsWith(`.${forbidden}`));
  if (defined) fail(`forbidden universal tool is defined: ${forbidden}`);
}

// A boolean "confirm" flag lets the requesting agent approve its own write.
// The only allowed confirmation mechanism is the confirmation_id protocol.
if (/"confirm"\s*:\s*\{\s*"type"\s*:\s*"boolean"/.test(toolsDoc)) {
  fail('docs/mcp-tools.md defines a boolean "confirm" schema property');
}

const docsToScan = [
  "docs/mcp-tools.md",
  "docs/integrations/magento.md",
  "docs/integrations/cloudflare.md",
  "docs/integrations/ga4.md",
  "docs/security/threat-model.md",
  "docs/security/security-review.md",
];
for (const file of docsToScan) {
  const text = readFileSync(join(root, file), "utf8");
  if (/confirm\s*=\s*true|"confirm"\s*:\s*true/.test(text)) {
    fail(`${file} still contains a self-approval confirm flag`);
  }
}

// ADR numbering must be unique and contiguous from 0001.
const adrDir = join(root, "docs/adr");
const adrNumbers = readdirSync(adrDir)
  .filter((name) => name.endsWith(".md"))
  .map((name) => {
    const match = /^(\d{4})-/.exec(name);
    if (!match) {
      fail(`ADR file has no four-digit number: ${name}`);
      return null;
    }
    return Number.parseInt(match[1], 10);
  })
  .filter((value) => value !== null)
  .sort((a, b) => a - b);

const duplicates = adrNumbers.filter((value, index) => index > 0 && value === adrNumbers[index - 1]);
if (duplicates.length > 0) fail(`duplicate ADR numbers: ${duplicates.join(", ")}`);
adrNumbers.forEach((value, index) => {
  if (value !== index + 1) fail(`ADR numbering gap at ${String(value).padStart(4, "0")}`);
});

// MCP conformance: the catalog must state the structured-content, resource-link,
// progress, and error semantics, and the conformance document must exist.
const conformance = join(root, "docs/mcp-conformance.md");
const conformanceText = readFileSync(conformance, "utf8");
for (const [label, needle] of [
  ["tools matrix", "## 2. Tools"],
  ["transport matrix", "## 3. Transport and sessions"],
  ["authorization matrix", "## 4. Authorization and security best practices"],
  ["test plan", "## 7. Test map"],
]) {
  if (!conformanceText.includes(needle)) fail(`docs/mcp-conformance.md is missing: ${label}`);
}
for (const needle of ["structuredContent", "resource_link", "progressToken", "isError"]) {
  if (!toolsDoc.includes(needle)) fail(`docs/mcp-tools.md does not state the MCP behavior: ${needle}`);
}

// Import jobs are queried through magento.import.status / .batch / .batches, not through
// platform.job.*; those tools were planned and never built, so the old assertion here
// demanded a name that does not exist. Assert what ships instead: a caller that starts a
// job must have a way to read it.
if ([...seen.keys()].some((name) => name.includes("import.start")) &&
    !seen.has("magento.import.status")) {
  fail("import.start exists but magento.import.status is missing");
}

const byModule = {};
for (const name of seen.keys()) {
  const module = name.split(".")[0];
  byModule[module] = (byModule[module] ?? 0) + 1;
}

if (failures.length > 0) {
  console.error(`Design contract check failed (${failures.length}):`);
  for (const failure of failures) console.error(`  - ${failure}`);
  process.exit(1);
}

console.log(`Design contracts OK: ${toolCount} tools, ${adrNumbers.length} ADRs`);
console.log(`  tools by module: ${JSON.stringify(byModule)}`);