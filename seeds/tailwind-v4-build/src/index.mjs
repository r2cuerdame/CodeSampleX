import postcss from 'postcss';
import tailwind from '@tailwindcss/postcss';

// v4 moved configuration INTO css: no tailwind.config.js, no
// @tailwind base/components/utilities. A single `@import "tailwindcss"`
// replaces the three directives, and design tokens live in @theme. A v3
// config file is not read, so a v3 project appears to build while emitting
// none of its customization.
export async function build(css, source) {
  const result = await postcss([tailwind()]).process(css, {
    from: 'input.css',
    map: false,
  });
  return result.css;
}
