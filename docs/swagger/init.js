window.addEventListener('DOMContentLoaded', () => {
  window.ui = SwaggerUIBundle({
    url: './openapi.json',
    dom_id: '#swagger-ui',
    deepLinking: true,
    docExpansion: 'list',
    displayRequestDuration: true,
    defaultModelsExpandDepth: 0,
    persistAuthorization: false,
    withCredentials: true,
    queryConfigEnabled: false,
    validatorUrl: null,
    supportedSubmitMethods: ['get', 'post'],
  });
});
