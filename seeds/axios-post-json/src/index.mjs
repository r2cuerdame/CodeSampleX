import axios from 'axios';

// Minimal axios.post usage: JSON in, parsed JSON out.
// axios sets content-type: application/json automatically for plain objects
// and parses a JSON response into res.data — no manual JSON.stringify/parse.
export async function postJSON(url, body) {
  const res = await axios.post(url, body);
  return { status: res.status, data: res.data };
}
