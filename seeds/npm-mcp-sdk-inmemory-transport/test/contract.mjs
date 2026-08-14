import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { DEFAULT_REQUEST_TIMEOUT_MSEC } from "@modelcontextprotocol/sdk/shared/protocol.js";
import {
  ErrorCode,
  LATEST_PROTOCOL_VERSION,
  McpError,
  SUPPORTED_PROTOCOL_VERSIONS,
} from "@modelcontextprotocol/sdk/types.js";

import { connectInMemory, createUnitsServer, rawExchange, SCALES } from "../src/server.mjs";

// The version this was measured against. It cannot be read the usual way: the
// package's exports map ends in a `"./*"` wildcard onto the built output, and
// package.json is not exempted from it, so `require(".../sdk/package.json")`
// resolves to dist/cjs/package.json and hands back the two-line stub that marks
// that directory as CommonJS. Version-gating code that reads it sees undefined.
const require = createRequire(import.meta.url);
assert.deepEqual(require("@modelcontextprotocol/sdk/package.json"), { type: "commonjs" });
const sdkManifest = JSON.parse(
  readFileSync(new URL("../node_modules/@modelcontextprotocol/sdk/package.json", import.meta.url), "utf8"),
);
assert.equal(sdkManifest.version, "1.30.0");

// The same wildcard is why every subpath keeps its file extension. Dropping it
// asks for dist/esm/inMemory, which is not a file, and the error names a path
// inside the package rather than the exports map that produced it.
await assert.rejects(import("@modelcontextprotocol/sdk/inMemory"), (error) => {
  assert.equal(error.code, "ERR_MODULE_NOT_FOUND");
  assert.ok(error.message.includes("@modelcontextprotocol/sdk/dist/esm/inMemory"));
  return true;
});

const { server, handlerCalls } = createUnitsServer();
const { client, clientTransport, wire } = await connectInMemory(server);

// ---------------------------------------------------------------------------
// The handshake
// ---------------------------------------------------------------------------

// Connecting a Client is the initialize round trip. Three messages, in this
// order, before any of your own code runs: the request, the response, and the
// notification that acknowledges it.
assert.deepEqual(
  wire.map((entry) => [entry.direction, entry.message.method ?? "(response)"]),
  [
    ["out", "initialize"],
    ["in", "(response)"],
    ["out", "notifications/initialized"],
  ],
);

const [initialize, initialized, ack] = wire.map((entry) => entry.message);

// The client asks for the newest version it knows and this server, being the
// same SDK build, agrees. That string is the answer to "which protocol version
// does 1.30.0 speak": 2025-11-25.
assert.equal(LATEST_PROTOCOL_VERSION, "2025-11-25");
assert.equal(initialize.params.protocolVersion, "2025-11-25");
assert.equal(initialized.result.protocolVersion, "2025-11-25");
assert.equal(initialized.id, initialize.id);

// The third message is a notification, so it carries no id and nothing replies
// to it. A client therefore has no way to know when the server finished
// reacting to it, only that it went out before connect() resolved.
assert.equal(ack.method, "notifications/initialized");
assert.equal("id" in ack, false);

// Reading it off the wire is not fastidiousness. The Client keeps the server's
// identity and capabilities but not the negotiated version: there is no
// getProtocolVersion(), the hook the HTTP transports use to record it,
// transport.setProtocolVersion, is not implemented by InMemoryTransport, and no
// own property of the client or the transport holds the string either.
assert.equal(client.getProtocolVersion, undefined);
assert.equal(clientTransport.setProtocolVersion, undefined);
assert.equal(
  [...Object.values(client), ...Object.values(clientTransport)].includes("2025-11-25"),
  false,
);
assert.deepEqual(client.getServerVersion(), { name: "units", version: "1.4.2" });
assert.equal(client.getInstructions(), undefined);

// Which is why recording the wire has to intercept `onmessage` through an
// accessor. connect() takes the transport over and overwrites the callbacks
// that are already sitting on it, so a plain assignment made beforehand is
// gone by the time the first message arrives.
const [spyClientSide, spyServerSide] = InMemoryTransport.createLinkedPair();
const marker = () => {};
spyClientSide.onmessage = marker;
spyClientSide.onclose = marker;
const spyClient = new Client({ name: "spy", version: "0.0.0" });
await createUnitsServer().server.connect(spyServerSide);
await spyClient.connect(spyClientSide);
assert.notEqual(spyClientSide.onmessage, marker);
assert.notEqual(spyClientSide.onclose, marker);
await spyClient.close();

// Capabilities come from what was registered, not from what the constructor was
// told. One registerTool and one registerResource produce exactly these two,
// each with listChanged, and no `prompts` key because no prompt was registered.
assert.deepEqual(client.getServerCapabilities(), {
  tools: { listChanged: true },
  resources: { listChanged: true },
});
assert.deepEqual(initialized.result.capabilities, client.getServerCapabilities());

// What the server does with a version it has never heard of: it does not
// refuse, it answers with its own latest and leaves the client to notice. An
// old client that only speaks 2024-11-05 therefore gets 2025-11-25 back and has
// to reject it itself — the SDK's client does, with a plain Error, but a
// hand-written one that trusts the response is now speaking the wrong protocol.
const askOld = await rawExchange(createUnitsServer().server, {
  jsonrpc: "2.0",
  id: 1,
  method: "initialize",
  params: { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: "raw", version: "0" } },
});
assert.equal(askOld[0].result.protocolVersion, "2024-11-05");
assert.ok(SUPPORTED_PROTOCOL_VERSIONS.includes("2024-11-05"));

const askUnknown = await rawExchange(createUnitsServer().server, {
  jsonrpc: "2.0",
  id: 1,
  method: "initialize",
  params: { protocolVersion: "1999-01-01", capabilities: {}, clientInfo: { name: "raw", version: "0" } },
});
assert.equal(askUnknown[0].error, undefined);
assert.equal(askUnknown[0].result.protocolVersion, LATEST_PROTOCOL_VERSION);

// The refusal the SDK's client performs, against a peer that answers with a
// version it has never heard of: connect() throws a plain Error carrying no
// code, so this failure is not distinguishable by an error code either.
const [oddClientSide, oddServerSide] = InMemoryTransport.createLinkedPair();
oddServerSide.onmessage = (message) => {
  oddServerSide.send({
    jsonrpc: "2.0",
    id: message.id,
    result: {
      protocolVersion: "1999-01-01",
      capabilities: {},
      serverInfo: { name: "odd", version: "0" },
    },
  });
};
await oddServerSide.start();
await assert.rejects(new Client({ name: "odd", version: "0.0.0" }).connect(oddClientSide), (error) => {
  assert.equal(error.constructor, Error);
  assert.equal(error.message, "Server's protocol version is not supported: 1999-01-01");
  assert.equal(error.code, undefined);
  return true;
});

// ---------------------------------------------------------------------------
// tools/list
// ---------------------------------------------------------------------------

const { tools } = await client.listTools();
assert.equal(tools.length, 1);
const [tool] = tools;
assert.equal(tool.name, "convert_temperature");
assert.equal(tool.title, "Convert temperature");
assert.equal(tool.description, "Convert a celsius reading into another scale.");

// The zod shape is published as draft-07 JSON Schema, with the `$schema` key
// inside it. A key with a .default() is absent from `required` and carries its
// default, so `required` is the two keys that have none.
assert.deepEqual(tool.inputSchema, {
  $schema: "http://json-schema.org/draft-07/schema#",
  type: "object",
  properties: {
    celsius: { type: "number" },
    scale: { type: "string", enum: SCALES },
    precision: { type: "integer", minimum: 0, maximum: 6, default: 1 },
  },
  required: ["celsius", "scale"],
});

// 1.30.0 stamps every tool definition with an `execution` block whose default
// forbids task augmentation. It is not something this server asked for, and a
// test that deep-equals a whole tool definition against a hand-written literal
// fails on it.
assert.deepEqual(tool.execution, { taskSupport: "forbidden" });

// ---------------------------------------------------------------------------
// A call that works
// ---------------------------------------------------------------------------

const boiling = await client.callTool({
  name: "convert_temperature",
  arguments: { celsius: 100, scale: "fahrenheit" },
});

// The result shape: `content` is an array of typed blocks, and a text block is
// exactly {type, text}. `isError` is not present-and-false on success, it is
// absent, so the check is `result.isError` and never `result.isError === false`.
assert.deepEqual(Object.keys(boiling), ["content"]);
assert.deepEqual(boiling.content, [{ type: "text", text: "212.0" }]);
assert.equal(Object.hasOwn(boiling, "isError"), false);

// The default arrived filled in rather than as undefined, which is why the
// handler can format with it directly.
assert.deepEqual(handlerCalls.at(-1), { celsius: 100, scale: "fahrenheit", precision: 1 });

// ---------------------------------------------------------------------------
// A call the schema rejects. This is the part people get wrong.
// ---------------------------------------------------------------------------

// The expectation going in was that a schema violation comes back as a JSON-RPC
// error and that `await client.callTool(...)` rejects. It does not. In 1.30.0
// the McpServer tools/call handler wraps its whole body in a try/catch and
// converts every McpError it raises into a CallToolResult with isError: true,
// so the transport-level answer is a successful response. A test written as
// `await assert.rejects(client.callTool(...))` fails, and worse, a caller that
// only checks for a thrown error treats the validation failure as success and
// reads content[0].text as if it were the tool's output.
const callsBefore = handlerCalls.length;
const badType = await client.callTool({
  name: "convert_temperature",
  arguments: { celsius: "hot", scale: "fahrenheit" },
});
assert.equal(badType.isError, true);
assert.equal(badType.content[0].type, "text");
assert.equal(
  badType.content[0].text,
  "MCP error -32602: Input validation error: Invalid arguments for tool convert_temperature: " +
    "Invalid input: expected number, received string at celsius",
);

// The JSON-RPC code survives only as text inside that message. There is no
// `code` field on the result to switch on, so telling a validation failure
// apart from a tool that legitimately failed means parsing English.
assert.equal(badType.code, undefined);
assert.equal(badType.error, undefined);
assert.ok(badType.content[0].text.includes(`MCP error ${ErrorCode.InvalidParams}`));
assert.equal(ErrorCode.InvalidParams, -32602);

// It really was rejected: the handler did not run. `callsBefore` was taken
// ahead of this call and is checked again after the three that follow, so all
// four schema failures are covered by it. That counter is the whole difference
// between a rejected call and a successful one, and it exists only inside the
// server — a client holding the result cannot see it.

// A missing required key and an out-of-range value come back the same way.
const missing = await client.callTool({ name: "convert_temperature", arguments: { celsius: 5 } });
assert.equal(missing.isError, true);
assert.ok(missing.content[0].text.endsWith('Invalid option: expected one of "fahrenheit"|"kelvin" at scale'));

const outOfRange = await client.callTool({
  name: "convert_temperature",
  arguments: { celsius: 5, scale: "kelvin", precision: 9 },
});
assert.equal(outOfRange.isError, true);
assert.ok(outOfRange.content[0].text.endsWith("Too big: expected number to be <=6 at precision"));

// Omitting `arguments` entirely is a validation failure too, not a call with no
// arguments: the schema sees undefined where it wanted an object.
const noArguments = await client.callTool({ name: "convert_temperature" });
assert.equal(noArguments.isError, true);
assert.ok(noArguments.content[0].text.endsWith("Invalid input: expected object, received undefined"));

assert.equal(handlerCalls.length, callsBefore);

// An extra property is not a violation. The object schema strips what it does
// not declare, the call succeeds, and the handler never sees the key — so a
// client sending a stale argument name gets silence rather than an error, and
// the tool runs with the field it was supposed to be setting left at default.
const extra = await client.callTool({
  name: "convert_temperature",
  arguments: { celsius: 0, scale: "kelvin", percision: 4 },
});
assert.deepEqual(extra.content, [{ type: "text", text: "273.1" }]);
assert.equal(Object.hasOwn(extra, "isError"), false);
assert.deepEqual(handlerCalls.at(-1), { celsius: 0, scale: "kelvin", precision: 1 });

// ---------------------------------------------------------------------------
// An unknown tool name, and where a real JSON-RPC error does come from
// ---------------------------------------------------------------------------

// Same channel as a validation failure: an isError result, not a rejection. The
// -32602 is again only inside the prose.
const unknownTool = await client.callTool({ name: "no_such_tool", arguments: {} });
assert.equal(unknownTool.isError, true);
assert.equal(unknownTool.content[0].text, "MCP error -32602: Tool no_such_tool not found");

// Resources answer on the other channel. An unregistered URI raises the same
// class of McpError inside the server, but the ReadResource handler has no
// try/catch over it, so it travels as a JSON-RPC error and the client's promise
// rejects. One server, two error conventions, decided by which handler you hit.
await assert.rejects(
  client.readResource({ uri: "config://units/nope" }),
  (error) => {
    assert.ok(error instanceof McpError);
    assert.equal(error.code, ErrorCode.InvalidParams);
    // The prefix appears twice: the server serialises an McpError by copying
    // its already-prefixed `message` into the JSON-RPC error, and the client
    // prefixes again when it rebuilds the McpError. Matching on the exact
    // string is therefore a trap of its own.
    assert.equal(
      error.message,
      "MCP error -32602: MCP error -32602: Resource config://units/nope not found",
    );
    return true;
  },
);

// So is an unregistered method. This server declared no prompts capability and
// prompts/list has no handler, so it comes back as MethodNotFound. Everything
// off the tools/call path reports its code as a code; only tools/call demotes
// one to prose.
await assert.rejects(client.listPrompts(), (error) => {
  assert.ok(error instanceof McpError);
  assert.equal(error.code, ErrorCode.MethodNotFound);
  assert.equal(error.code, -32601);
  assert.equal(error.message, "MCP error -32601: Method not found");
  return true;
});

// ---------------------------------------------------------------------------
// A handler that throws
// ---------------------------------------------------------------------------

// The throw is caught and turned into an isError result, and the message is the
// bare Error message with no "MCP error" prefix — which is how you can tell a
// tool that failed from a call the framework rejected, and the only way you can.
const belowZero = await client.callTool({
  name: "convert_temperature",
  arguments: { celsius: -300, scale: "kelvin" },
});
assert.equal(belowZero.isError, true);
assert.deepEqual(belowZero.content, [{ type: "text", text: "-300 C is below absolute zero" }]);
assert.equal(belowZero.content[0].text.startsWith("MCP error"), false);

// The connection is untouched. The next call works, on the same client, over
// the same transport, with the handler running again.
const after = await client.callTool({
  name: "convert_temperature",
  arguments: { celsius: 0, scale: "kelvin", precision: 2 },
});
assert.deepEqual(after.content, [{ type: "text", text: "273.15" }]);
assert.equal(handlerCalls.at(-1).precision, 2);

// ---------------------------------------------------------------------------
// The resource, and the fact that the pair is not a wire
// ---------------------------------------------------------------------------

const { resources } = await client.listResources();
assert.deepEqual(resources, [
  {
    name: "supported-scales",
    title: "Supported scales",
    uri: "config://units/scales",
    mimeType: "application/json",
  },
]);

const read = await client.readResource({ uri: "config://units/scales" });
assert.deepEqual(read.contents, [
  {
    uri: "config://units/scales",
    mimeType: "application/json",
    text: '["fahrenheit","kelvin"]',
  },
]);

// InMemoryTransport calls the peer's onmessage with the object itself. Nothing
// is stringified anywhere on this path, so values JSON cannot carry survive:
// the Date the resource put in _meta arrives as a Date. Run the same server
// over stdio and the client reads the ISO string instead, and any code that
// called .getTime() on it starts throwing in production only.
assert.ok(read._meta.revisedAt instanceof Date);
assert.equal(read._meta.revisedAt.toISOString(), "2024-01-01T00:00:00.000Z");
assert.equal(JSON.parse(JSON.stringify(read))._meta.revisedAt, "2024-01-01T00:00:00.000Z");

// The same absence of a JSON round trip keeps undefined-valued keys alive. This
// server set no annotations, and over a real transport the key would be dropped
// by JSON.stringify; here it is an own property holding undefined, so
// `"annotations" in tool` is true and `Object.keys` reports it.
assert.equal(Object.hasOwn(tool, "annotations"), true);
assert.equal(tool.annotations, undefined);
assert.ok(Object.keys(tool).includes("annotations"));
assert.equal(Object.hasOwn(JSON.parse(JSON.stringify(tool)), "annotations"), false);

// ---------------------------------------------------------------------------
// Connecting in the wrong order, and closing
// ---------------------------------------------------------------------------

// The client half can be started with nothing on the other end: the initialize
// request lands on the server half's queue instead of being lost, and nothing
// fails fast to tell you the peer is missing. Two hundred milliseconds in, the
// promise is still pending.
const late = createUnitsServer().server;
const [lateClientSide, lateServerSide] = InMemoryTransport.createLinkedPair();
const lateClient = new Client({ name: "late", version: "0.0.0" });
const connecting = lateClient.connect(lateClientSide);
let settled = false;
connecting.then(() => { settled = true; }, () => { settled = true; });
await new Promise((resolve) => setTimeout(resolve, 200));
assert.equal(settled, false);
assert.equal(lateClient.getServerCapabilities(), undefined);

// Connecting the server drains the queue and the handshake finishes, which is
// what makes "start both, await afterwards" a legitimate pattern.
await late.connect(lateServerSide);
await connecting;
assert.deepEqual(lateClient.getServerCapabilities(), {
  tools: { listChanged: true },
  resources: { listChanged: true },
});

// What it does not do is wait indefinitely, and that is the part worth getting
// right: every request the Protocol sends carries a timeout, initialize
// included, so a client connected to nothing eventually rejects with
// RequestTimeout rather than hanging the run. Sitting out the default takes a
// minute — measured once, the rejection lands just past 60 s — so the same
// path is driven here with an explicit 300 ms. The rejection is an McpError, so
// this failure does carry a code to switch on, and the timeout it gave up on is
// in `data`.
assert.equal(DEFAULT_REQUEST_TIMEOUT_MSEC, 60000);
const [orphanSide] = InMemoryTransport.createLinkedPair();
await assert.rejects(
  new Client({ name: "orphan", version: "0.0.0" }).connect(orphanSide, { timeout: 300 }),
  (error) => {
    assert.ok(error instanceof McpError);
    assert.equal(error.code, ErrorCode.RequestTimeout);
    assert.equal(error.code, -32001);
    assert.equal(error.message, "MCP error -32001: Request timed out");
    assert.deepEqual(error.data, { timeout: 300 });
    return true;
  },
);

// Tear-down runs in both directions, and closing the client half is the one
// that is easy to miss: close() drops the link and closes the peer, so the
// server's own onclose fires and it goes disconnected without anyone having
// called close() on it.
let lateServerClosed = false;
late.server.onclose = () => { lateServerClosed = true; };
assert.equal(late.isConnected(), true);
await lateClient.close();
assert.equal(lateServerClosed, true);
assert.equal(late.isConnected(), false);

// The other direction, from the server this time, and the failure the client
// then gives you is a plain Error with no code on it — not an McpError
// carrying ErrorCode.ConnectionClosed, which is what a catch block written
// against the error codes would be looking for, and not the RequestTimeout
// above either.
await server.close();
await assert.rejects(
  client.callTool({ name: "convert_temperature", arguments: { celsius: 0, scale: "kelvin" } }),
  (error) => {
    assert.equal(error.constructor, Error);
    assert.ok(!(error instanceof McpError));
    assert.equal(error.message, "Not connected");
    assert.equal(error.code, undefined);
    assert.notEqual(ErrorCode.ConnectionClosed, undefined);
    return true;
  },
);
await client.close();

console.log("contract ok: mcp sdk 1.30.0, protocol", LATEST_PROTOCOL_VERSION);
