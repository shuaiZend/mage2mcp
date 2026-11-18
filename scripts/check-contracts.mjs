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
const NAME_RE = /^(magento|cloudflare|ga4|unified|platform)\.[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$/;
const SCOPES = new Set(["read", "write", "destructive", "admin"]);
const FORBIDDEN_TOOLS = ["execute_api", "execute_sql", "raw_graphql", "run_sql"];

const toolsDoc = readFileSync(join(root, "docs/mcp-tools.md"), "utf8");
const seen = new Map();
let toolCount = 0;

for (const match of toolsDoc.matchAll(TOOL_RE)) {
  const [, name, scope] = match;
  toolCount += 1;
  if (!NAME_RE.test(name)) fail(`tool name does not match the naming grammar: ${name}`);
  if (!SCOPES.has(scope)) fail(`tool ${name} has unknown scope "${scope}"`);
  if (seen.has(name)) fail(`duplicate tool name: ${name}`);
  seen.set(name, scope);
}

if (toolCount === 0) fail("no tools found in docs/mcp-tools.md; the table format changed");

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

// Every tool that returns a job handle must have job tools available.
if ([...seen.keys()].some((name) => name.includes("import.start")) &&
    !seen.has("platform.job.get")) {
  fail("import tools exist but platform.job.get is missing");
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