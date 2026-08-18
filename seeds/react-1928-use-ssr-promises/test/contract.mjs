import assert from 'node:assert/strict';
import { Writable } from 'node:stream';
import React, { Suspense, use } from 'react';
import { renderToPipeableStream, renderToString } from 'react-dom/server';

function Value({ resource }) {
  return React.createElement('strong', null, use(resource));
}

function app(resource, withShell = false) {
  const boundary = React.createElement(
    Suspense,
    { fallback: React.createElement('i', null, 'loading') },
    React.createElement(Value, { resource })
  );
  return withShell ? React.createElement('main', null, 'shell:', boundary) : boundary;
}

const resolved = Promise.resolve('ready');
const firstResolvedHTML = renderToString(app(resolved));
assert.equal(resolved.status, 'pending');
assert.match(firstResolvedHTML, /<i>loading<\/i>/);
assert.match(firstResolvedHTML, /renderToString/);
assert.match(firstResolvedHTML, /does not support Suspense/);

await new Promise(resolve => setImmediate(resolve));
assert.equal(resolved.status, 'fulfilled');
assert.equal(resolved.value, 'ready');
assert.equal(renderToString(app(resolved)), '<!--$--><strong>ready</strong><!--/$-->');

const rejected = Promise.reject(new Error('resource failed'));
rejected.catch(() => {});
const firstRejectedHTML = renderToString(app(rejected));
assert.equal(rejected.status, 'pending');
assert.match(firstRejectedHTML, /<i>loading<\/i>/);

await new Promise(resolve => setImmediate(resolve));
assert.equal(rejected.status, 'rejected');
assert.equal(rejected.reason.message, 'resource failed');
const secondRejectedHTML = renderToString(app(rejected));
assert.match(secondRejectedHTML, /<i>loading<\/i>/);
assert.match(secondRejectedHTML, /resource failed/);
assert.match(secondRejectedHTML, /Switched to client rendering because the server rendering errored/);

let resolveStreamingResource;
const streamingResource = new Promise(resolve => {
  resolveStreamingResource = resolve;
});
const chunks = [];
const errors = [];
let shellReady = false;
let allReady = false;
let resourceResolved = false;
let controls;

const completed = new Promise((resolve, reject) => {
  const destination = new Writable({
    write(chunk, _encoding, callback) {
      chunks.push(chunk.toString());
      if (!resourceResolved) {
        resourceResolved = true;
        setImmediate(() => resolveStreamingResource('streamed'));
      }
      callback();
    }
  });
  destination.once('finish', resolve);
  destination.once('error', reject);

  controls = renderToPipeableStream(app(streamingResource, true), {
    onShellReady() {
      shellReady = true;
      controls.pipe(destination);
    },
    onAllReady() {
      allReady = true;
    },
    onShellError: reject,
    onError(error) {
      errors.push(error);
    }
  });
});

await completed;
assert.equal(shellReady, true);
assert.equal(allReady, true);
assert.deepEqual(errors, []);
assert.ok(chunks.length >= 2, `chunks = ${chunks.length}`);
assert.match(chunks[0], /shell:/);
assert.match(chunks[0], /<i>loading<\/i>/);
assert.doesNotMatch(chunks[0], /<strong>streamed<\/strong>/);

const streamedHTML = chunks.join('');
assert.match(streamedHTML, /<strong>streamed<\/strong>/);
assert.match(streamedHTML, /hidden/);
assert.equal(streamingResource.status, 'fulfilled');
assert.equal(streamingResource.value, 'streamed');

console.log('CONTRACT PASS: React 19.2.8 use() promise SSR boundaries are measured');
