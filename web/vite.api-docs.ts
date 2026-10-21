import type { Plugin, PreviewServer, ViteDevServer } from 'vite';

/** Vite's public-file middleware does not resolve directory indexes. */
export function apiDocsIndex(): Plugin {
  const configure = (server: ViteDevServer | PreviewServer) => {
    server.middlewares.use((request, response, next) => {
      const path = request.url?.split('?')[0];
      if (path === '/api-docs') {
        response.writeHead(302, { Location: '/api-docs/' });
        response.end();
        return;
      }
      if (path === '/api-docs/') request.url = '/api-docs/index.html';
      next();
    });
  };
  return { name: 'local-api-docs-index', configureServer: configure, configurePreviewServer: configure };
}
