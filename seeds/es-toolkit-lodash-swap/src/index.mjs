import { chunk, groupBy, pick, debounce } from 'es-toolkit';

// es-toolkit is a drop-in for the lodash functions most projects actually
// use, with native ESM and no CommonJS interop dance. The behaviours worth
// checking before swapping: groupBy keys are strings, pick ignores absent
// keys rather than inventing undefined entries, and debounce coalesces.
export function pageRows(rows, size) {
  return chunk(rows, size);
}

export function byStatus(rows) {
  return groupBy(rows, (r) => r.status);
}

export function summarize(row) {
  return pick(row, ['id', 'status']);
}

export function makeDebounced(fn, ms) {
  return debounce(fn, ms);
}
