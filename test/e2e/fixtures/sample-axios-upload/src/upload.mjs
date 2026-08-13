// Minimal axios.post upload pattern (clean-room sample; no project code).
// The contract uses node's built-in http to stay dependency-light while the
// sample documents the axios call shape.
export async function upload(axios, url, body) {
  const res = await axios.post(url, body, {
    headers: { 'content-type': 'application/json' },
  });
  return res.data;
}
