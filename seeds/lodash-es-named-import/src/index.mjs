import { chunk, groupBy, pick } from 'lodash-es';

// lodash-es is the ESM build: named imports work directly and bundlers can
// drop what you do not use. The CJS 'lodash' package has no named exports in
// ESM, so `import { chunk } from 'lodash'` fails at load time instead.
export function pageRows(rows, size) {
  return chunk(rows, size);
}

export function byStatus(rows) {
  return groupBy(rows, (r) => r.status);
}

export function summarize(row) {
  return pick(row, ['id', 'status']);
}
