import { mkdir, copyFile, readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../', import.meta.url));
const output = join(web, 'public/api-docs');
const swagger = dirname(createRequire(import.meta.url).resolve('swagger-ui-dist/package.json'));
const specification = new URL('../../docs/api/openapi.json', import.meta.url);
JSON.parse(await readFile(specification, 'utf8'));
await mkdir(output, { recursive: true });
for (const name of ['swagger-ui.css', 'swagger-ui-bundle.js', 'LICENSE', 'NOTICE']) {
  await copyFile(join(swagger, name), join(output, name));
}
await copyFile(specification, join(output, 'openapi.json'));
for (const name of ['index.html', 'init.js', 'docs.css']) {
  await copyFile(new URL(`../../docs/swagger/${name}`, import.meta.url), join(output, name));
}
console.log('Local Swagger UI prepared at /api-docs/ (no CDN or remote validator).');
