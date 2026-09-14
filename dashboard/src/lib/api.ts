// JSON fetch that fails with a sentence instead of a parser error.
//
// nginx serves the dashboard with `try_files $uri $uri/ /index.html`, so ANY
// path it does not have a proxy rule for answers 200 text/html with the SPA
// shell. A caller that does `res.ok && res.json()` on such a path gets
// `SyntaxError: Unexpected token '<', "<!doctype "... is not valid JSON` and
// no clue which request produced it — which is exactly how a route deleted
// from the Go router (`/services`, removed in favour of /api/neighbors) sat
// broken on the dashboard.
//
// So: check the content type, and name the URL in every error.

export async function getJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init);
  const ct = res.headers.get('content-type') || '';

  if (!res.ok) {
    throw new Error(`${url} → HTTP ${res.status}`);
  }
  if (!ct.includes('application/json')) {
    // The SPA fallback is the overwhelmingly likely cause; say so.
    throw new Error(
      `${url} → ${res.status} ${ct || 'no content-type'} (not JSON — the route is ` +
        `missing and nginx served the dashboard shell instead)`
    );
  }
  return (await res.json()) as T;
}
