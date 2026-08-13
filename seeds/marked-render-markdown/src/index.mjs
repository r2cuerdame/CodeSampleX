import { marked } from 'marked';

// marked removed its `sanitize` option: it renders Markdown, it is not an
// HTML sanitizer. Raw HTML in the input reaches the output verbatim, so
// untrusted markdown must go through a sanitizer (DOMPurify and friends)
// afterwards. Treating marked's output as safe is the most common way this
// package turns into an XSS hole.
export function render(md) {
  return marked.parse(md, { async: false });
}
