import axios from 'axios';
import lib from 'local-lib';

export async function upload(url, data) {
  const res = await axios.post(url, data);
  lib.log(res.status);
  return res.data;
}
