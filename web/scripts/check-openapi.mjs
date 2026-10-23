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
assert.deepEqual(documented, registered.sort(), 'OpenAPI operations must match all registered public routes');
const ids = Object.values(spec.paths).flatMap(methods => Object.values(methods).map(operation => operation.operationId));
assert.equal(new Set(ids).size, ids.length, 'operationId must be unique');
assert.deepEqual(spec.servers.map(server => server.url), ['/'], 'Interactive requests must use the current origin');
console.log(`OpenAPI valid: ${documented.length} operations match Go routes; all references are local.`);
