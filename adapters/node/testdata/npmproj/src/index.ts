import axios from 'axios';
import { AxiosError, isAxiosError as isErr } from 'axios';

export async function ping(url: string) {
  const res = await axios.post(url, { ok: true });
  if (isErr(res)) {
    throw new AxiosError('request failed');
  }
  return res;
}
