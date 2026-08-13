import { createElement as h } from 'react';
import { renderToStaticMarkup, renderToString } from 'react-dom/server';

// react-dom/server is a SEPARATE entry point; importing renderToString
// from 'react-dom' fails.
//
// The two renderers differ in one specific place: where a text node sits
// next to another text node, renderToString emits a `<!-- -->` separator
// so hydration can find the boundary between them. renderToStaticMarkup
// omits it. Markup that will be hydrated must come from renderToString —
// using static markup saves a few bytes and then fails hydration on
// exactly those boundaries. A tree with no adjacent text nodes produces
// identical output from both, which is why the difference is easy to miss
// in a small test and only shows up in real content.
function Line({ user, count }) {
  // "signed in as " and {user} are adjacent text nodes: the boundary case.
  return h('p', null, 'signed in as ', user, ' (', count, ' items)');
}

export function hydratableHTML(props) {
  return renderToString(h(Line, props));
}

export function staticHTML(props) {
  return renderToStaticMarkup(h(Line, props));
}

export function escaped(text) {
  return renderToStaticMarkup(h('div', null, text));
}
