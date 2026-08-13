import { request } from 'undici';

// undici.request does not serialize or parse for you: the body must be a
// string and the response body is a stream with .json()/.text() helpers.
// Skipping await on body.json() leaks the connection back into the pool
// unread, which stalls later requests.
export async function postJSON(url, payload) {
  const { statusCode, body } = await request(url, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(payload),
  });
  return { statusCode, data: await body.json() };
}
