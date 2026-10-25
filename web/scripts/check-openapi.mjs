import SwaggerParser from '@apidevtools/swagger-parser';
import { readFile, readdir } from 'node:fs/promises';
import assert from 'node:assert/strict';

const root = new URL('../../', import.meta.url);
const spec = JSON.parse(await readFile(new URL('docs/api/openapi.json', root), 'utf8'));
// All refs must stay inside the tracked document; validation must not fetch remote files.
function inspect(value) {
  if (!value || typeof value !== 'object') return;
  if ('$ref' in value) assert.match(value.$ref, /^#\/components\//);
  for (const child of Object.values(value)) inspect(child);
}
inspect(spec);
await SwaggerParser.validate(structuredClone(spec), { resolve: { external: false } });
const documented = Object.entries(spec.paths).flatMap(([path, methods]) => Object.keys(methods).map(method => `${method.toUpperCase()} ${path}`)).sort();
// The human reference must list every implemented operation, too. A valid
// OpenAPI alone cannot catch stale/missing rows in the Markdown guide.
const reference = await readFile(new URL('docs/api/README.md', root), 'utf8');
const referenceOperations = [...reference.matchAll(/^\|\s*(GET|POST|PATCH|PUT|DELETE|HEAD|OPTIONS|TRACE)\s*\|\s*`([^`]+)`\s*\|/gm)]
  .map(match => `${match[1]} ${match[2]}`).sort();
assert.deepEqual(referenceOperations, documented, 'API reference table must match every OpenAPI operation without duplicates');
const registered = [];
async function goFiles(directory) {
  const entries = await readdir(new URL(directory, root), { withFileTypes: true });
  const files = await Promise.all(entries.map(entry => entry.isDirectory() ? goFiles(`${directory}${entry.name}/`)
    : entry.name.endsWith('.go') && !entry.name.endsWith('_test.go') ? [`${directory}${entry.name}`] : []));
  return files.flat();
}
for (const path of ['cmd/poaktu/main.go', ...await goFiles('internal/features/')]) {
  const source = await readFile(new URL(path, root), 'utf8');
  for (const match of source.matchAll(/mux\.Handle(?:Func)?\("((?:GET|POST|PATCH|PUT|DELETE) [^"]+)"/g)) registered.push(match[1]);
}
assert.deepEqual(documented, registered.sort(), 'OpenAPI operations must match all registered HTTP routes');
const ids = Object.values(spec.paths).flatMap(methods => Object.values(methods).map(operation => operation.operationId));
assert.equal(new Set(ids).size, ids.length, 'operationId must be unique');
assert.deepEqual(spec.servers.map(server => server.url), ['/'], 'Interactive requests must use the current origin');
console.log(`OpenAPI valid: ${documented.length} operations match Go routes; all references are local.`);
console.log(`API reference valid: ${referenceOperations.length} table rows match OpenAPI.`);
