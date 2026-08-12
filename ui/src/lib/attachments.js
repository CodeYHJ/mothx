// Client-side attachment URL policy. Provider-backed downloads use the local
// API proxy; arbitrary attachment URLs are only rendered when they are safe
// external HTTPS URLs.

export function safeAttachmentURL(value) {
  try {
    const parsed = new URL(String(value || ''));
    if (parsed.protocol !== 'https:' || parsed.username || parsed.password) return '';
    const host = parsed.hostname.toLowerCase().replace(/^\[|\]$/g, '').replace(/\.$/, '');
    if (!host || host === 'localhost' || host.endsWith('.localhost')) return '';
    if (host === '0.0.0.0' || host === '::' || host === '::1' || host.startsWith('fe80:') || host.startsWith('fc') || host.startsWith('fd')) return '';
    if (/^127\./.test(host) || /^10\./.test(host) || /^192\.168\./.test(host)) return '';
    const octets = host.match(/^172\.(\d{1,3})\./);
    if (octets && Number(octets[1]) >= 16 && Number(octets[1]) <= 31) return '';
    return parsed.href;
  } catch {
    return '';
  }
}

export function validProviderRef(value) {
  return typeof value === 'string' && value.length > 0 && value.length <= 2048 && !/[\u0000\r\n]/.test(value);
}
