// URL patterns whose requests originate from the bundled Web UI and require the
// desktop-generated bearer token. WebSocket handshakes use ws:// rather than
// http://, so both schemes must be registered with Electron's webRequest API.
export function localServeRequestURLPatterns(port: number): string[] {
  const host = `127.0.0.1:${port}`;
  return [
    `http://${host}/*`,
    `ws://${host}/*`,
  ];
}
