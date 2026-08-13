import axios from 'axios';

// Node 18+ has FormData and Blob globally, so axios needs no form-data
// package: given a FormData body it sets multipart/form-data and the
// boundary itself. Setting content-type by hand here would drop the
// boundary and the server would fail to parse the request.
export async function uploadFile(url, filename, content) {
  const form = new FormData();
  form.append('file', new Blob([content], { type: 'text/plain' }), filename);
  form.append('kind', 'sample');
  const res = await axios.post(url, form);
  return res.data;
}
