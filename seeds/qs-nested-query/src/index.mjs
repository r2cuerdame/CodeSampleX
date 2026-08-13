import qs from 'qs';

// URLSearchParams is flat: every value is a string and a[b]=c is a key
// literally called "a[b]". qs understands the bracket convention that most
// web frameworks emit. It also defaults to a depth limit of 5 — deeply
// nested hostile input is truncated rather than allowed to explode memory.
export function parse(query, options = {}) {
  return qs.parse(query, options);
}

export function build(obj) {
  return qs.stringify(obj);
}
