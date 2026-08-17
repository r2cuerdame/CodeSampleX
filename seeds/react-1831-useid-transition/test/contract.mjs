import assert from "node:assert/strict";
import React, { startTransition, useId } from "react";
import { renderToString } from "react-dom/server";


const order = [];
const transitionResult = startTransition(() => {
  order.push("inside");
  return "ignored";
});
order.push("after");

assert.deepEqual(order, ["inside", "after"]);
assert.equal(transitionResult, undefined);
assert.throws(
  () => startTransition(() => {
    throw new Error("transition failure");
  }),
  /transition failure/,
);

function Fields() {
  const inputId = useId();
  const helpId = useId();
  return React.createElement(
    "section",
    null,
    React.createElement("label", { htmlFor: inputId }, "Value"),
    React.createElement("input", { id: inputId, "aria-describedby": helpId }),
    React.createElement("small", { id: helpId }, "Help"),
  );
}

function idsIn(html) {
  return [...html.matchAll(/\sid="([^"]+)"/g)].map((match) => match[1]);
}

const firstHTML = renderToString(React.createElement(Fields));
const firstIDs = idsIn(firstHTML);
assert.equal(firstIDs.length, 2);
assert.notEqual(firstIDs[0], firstIDs[1]);
assert.match(firstHTML, new RegExp(`for="${firstIDs[0]}"`));
assert.match(firstHTML, new RegExp(`aria-describedby="${firstIDs[1]}"`));

const repeatedHTML = renderToString(React.createElement(Fields));
assert.deepEqual(idsIn(repeatedHTML), firstIDs);

const prefixedHTML = renderToString(
  React.createElement(Fields),
  { identifierPrefix: "sample-" },
);
const prefixedIDs = idsIn(prefixedHTML);
assert.equal(prefixedIDs.length, 2);
assert.ok(prefixedIDs.every((id) => id.includes("sample-")));
assert.notDeepEqual(prefixedIDs, firstIDs);

console.log("CONTRACT PASS: React 18.3.1 useId and startTransition behavior");
