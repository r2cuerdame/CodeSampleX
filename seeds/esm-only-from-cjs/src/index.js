'use strict';

/**
 * Using ESM-only npm packages from a CommonJS project.
 *
 * yoga-layout@3 and p-limit@7 are both ESM-only: "type": "module" with no
 * `require` condition in their exports map. A CommonJS require() of either one
 * is wrong -- but on current Node they are wrong in two different ways, and
 * only one of them is still loud. See src/broken-require.js.
 *
 *   Node <  22.12   require() -> ERR_REQUIRE_ESM for both packages.
 *   Node >= 22.12   require('p-limit')     -> succeeds, and returns the ES
 *                                             module NAMESPACE object, so the
 *                                             usual `pLimit(2)` is a TypeError
 *                                             somewhere else entirely.
 *                   require('yoga-layout') -> ERR_REQUIRE_ASYNC_MODULE. Its
 *                                             graph awaits the WebAssembly
 *                                             instantiation at the top level,
 *                                             and a synchronous require() has
 *                                             nothing to block on. No Node
 *                                             version can load that with
 *                                             require().
 *
 * One pattern covers every case, on every Node >= 12.17: dynamic import()
 * inside an async function. import() returns a Promise, so it works unchanged
 * in a `require`-shaped file, and it never needs the package to ship a CJS
 * build. (The other fix is to convert the consumer to ESM -- rename to .mjs or
 * set "type": "module" -- which is the better move if you can afford it.)
 *
 * Two details that bite people:
 *
 *   - Cache the PROMISE, not the resolved module. CommonJS has no top-level
 *     await, so there is nothing to assign at module scope. Awaiting an
 *     already-settled promise is free, so the loader stays cheap to call on
 *     every request instead of only at startup.
 *
 *   - import() resolves to the module NAMESPACE, not to the default export.
 *     `export default` lands on `.default`. Destructure it:
 *         const { default: pLimit } = await import('p-limit');
 *     This is the same shape require() now hands back on Node >= 22.12, which
 *     is why the `TypeError: pLimit is not a function` reports are suddenly
 *     more common than ERR_REQUIRE_ESM.
 */

let yogaPromise;
let pLimitPromise;

/** @returns {Promise<object>} the yoga-layout module namespace (cached). */
function loadYoga() {
  if (!yogaPromise) yogaPromise = import('yoga-layout');
  return yogaPromise;
}

/** @returns {Promise<object>} the p-limit module namespace (cached). */
function loadPLimit() {
  if (!pLimitPromise) pLimitPromise = import('p-limit');
  return pLimitPromise;
}

/**
 * Lay out `count` equal-width columns inside a `width` x `height` box with
 * `gap` pixels between them, and return each column's computed geometry.
 */
async function rowLayout({ width, height, count, gap }) {
  // `Yoga` is the default export; Direction/Edge/FlexDirection are named
  // exports. Both come off the same namespace object.
  const { default: Yoga, Direction, Edge, FlexDirection } = await loadYoga();

  const root = Yoga.Node.create();
  root.setWidth(width);
  root.setHeight(height);
  root.setFlexDirection(FlexDirection.Row);

  const kids = [];
  for (let i = 0; i < count; i += 1) {
    const kid = Yoga.Node.create();
    kid.setFlexGrow(1);
    if (i > 0) kid.setMargin(Edge.Left, gap);
    root.insertChild(kid, i);
    kids.push(kid);
  }

  root.calculateLayout(width, height, Direction.LTR);
  const boxes = kids.map((kid) => {
    const box = kid.getComputedLayout();
    return { left: box.left, top: box.top, width: box.width, height: box.height };
  });

  root.freeRecursive(); // yoga nodes are wasm-backed; free them explicitly
  return boxes;
}

/**
 * Run every task with at most `concurrency` of them in flight at once,
 * resolving to the results in input order.
 */
async function runLimited(tasks, concurrency) {
  const { default: pLimit } = await loadPLimit();
  const limit = pLimit(concurrency);
  return Promise.all(tasks.map((task) => limit(task)));
}

module.exports = { loadPLimit, loadYoga, rowLayout, runLimited };
