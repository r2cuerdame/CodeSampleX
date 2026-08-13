import axios from 'axios';

// A timeout rejects with err.code === 'ECONNABORTED' (message: "timeout of
// Nms exceeded"). Branching on the message is brittle across versions and
// locales; the code is the stable signal. err.response is undefined here —
// nothing came back — so err.response.status would itself throw.
export async function getWithTimeout(url, ms) {
  try {
    const res = await axios.get(url, { timeout: ms });
    return { timedOut: false, status: res.status };
  } catch (err) {
    return { timedOut: err.code === 'ECONNABORTED', code: err.code, hasResponse: Boolean(err.response) };
  }
}
