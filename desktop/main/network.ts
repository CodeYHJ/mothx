// URL patterns whose requests originate from the bundled Web UI and require the
// desktop-generated bearer token. Keep this limited to API and WebSocket traffic;
// Keep this limited to the bundled UI's navigation/static assets plus API and
// WebSocket traffic. Do not match every local URL: a same-origin redirect or
// unexpected resource must not automatically receive the bearer token.
export function localServeRequestURLPatterns(port: number): string[] {
  const host = `127.0.0.1:${port}`;
  return [
    `http://${host}/`,
    `http://${host}/assets/*`,
    `http://${host}/mothx-small.ico`,
    `http://${host}/api/*`,
    `http://${host}/v1/*`,
    `http://${host}/health`,
    `ws://${host}/ws/*`,
  ];
}
