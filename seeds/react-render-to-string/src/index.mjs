import { createElement as h } from 'react';
import { renderToStaticMarkup, renderToString } from 'react-dom/server';

// react-dom/server is a SEPARATE entry point from react-dom; importing
// renderToString from 'react-dom' fails. Two functions, different jobs:
// renderToString emits the comment markers hydration needs, while
// renderToStaticMarkup emits plain HTML for output that is never hydrated
// (email, static pages) — using the wrong one leaves stray markers or
// breaks hydration.
function Card({ title, note }) {
  return h('div', { className: 'card' }, h('h2', null, title), h('p', null, note));
}

export function staticHTML(props) {
  return renderToStaticMarkup(h(Card, props));
}

export function hydratableHTML(props) {
  return renderToString(h(Card, props));
}
