import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";

/**
 * Testing an MCP server without a process, a pipe, or a port.
 *
 * The SDK ships the transport for this itself:
 * `InMemoryTransport.createLinkedPair()` returns two halves that hand messages
 * straight to each other. One goes to a `Server`/`McpServer`, the other to a
 * `Client`, and the whole initialize handshake runs in the same event loop as
 * the test. There is no stdio to drain, no child process to kill, and nothing
 * to await except the calls themselves.
 *
 * The subpath is `@modelcontextprotocol/sdk/inMemory.js`, with the `.js` — the
 * package's exports map is a wildcard onto the built files, so the extension is
 * part of the specifier and `.../inMemory` does not resolve.
 */

export const SCALES = ["fahrenheit", "kelvin"];

/**
 * One tool and one resource, which is enough to pin down every error channel
 * the protocol has.
 *
 * Registering them is also what declares the capabilities: `registerTool` turns
 * on `tools.listChanged` and `registerResource` turns on
 * `resources.listChanged`, so the capabilities the client sees during
 * initialize are derived from what you registered, not from anything you pass
 * to the constructor. A server with no prompts advertises no `prompts`
 * capability at all, and the difference is load-bearing — see the contract,
 * where an unregistered method is the only thing in this sample that produces a
 * JSON-RPC MethodNotFound.
 *
 * `handlerCalls` is returned alongside so the contract can prove the handler
 * did not run for a rejected call. Whether the handler ran is the whole
 * question when the failure comes back looking like a successful response.
 */
export function createUnitsServer() {
  const handlerCalls = [];
  const server = new McpServer({ name: "units", version: "1.4.2" });

  server.registerTool(
    "convert_temperature",
    {
      title: "Convert temperature",
      description: "Convert a celsius reading into another scale.",
      inputSchema: {
        celsius: z.number(),
        scale: z.enum(SCALES),
        // A key with a default is optional on the wire and always present in
        // the handler: it is absent from the JSON Schema's `required` list and
        // arrives filled in, so the handler never has to re-apply the default.
        precision: z.number().int().min(0).max(6).default(1),
      },
    },
    async (args) => {
      handlerCalls.push(args);
      const { celsius, scale, precision } = args;
      // A domain failure the schema cannot express. Throwing from a tool
      // handler is the ordinary way to report one; the contract measures what
      // the client receives.
      if (celsius < -273.15) {
        throw new Error(`${celsius} C is below absolute zero`);
      }
      const converted = scale === "kelvin" ? celsius + 273.15 : (celsius * 9) / 5 + 32;
      return { content: [{ type: "text", text: converted.toFixed(precision) }] };
    },
  );

  server.registerResource(
    "supported-scales",
    "config://units/scales",
    { title: "Supported scales", mimeType: "application/json" },
    async (uri) => ({
      contents: [
        {
          uri: uri.href,
          mimeType: "application/json",
          text: JSON.stringify(SCALES),
        },
      ],
      // A Date on purpose. Over stdio or HTTP this is JSON, so the client would
      // read the string "2024-01-01T00:00:00.000Z"; across the linked pair the
      // Date object itself arrives. The contract measures that, because it is
      // the way an in-memory test passes while the real transport fails.
      _meta: { revisedAt: new Date("2024-01-01T00:00:00.000Z") },
    }),
  );

  return { server, handlerCalls };
}

/**
 * Records every message crossing one half of the pair.
 *
 * Worth having because the SDK's `Client` gives you no way to ask what protocol
 * version was negotiated — there is no `getProtocolVersion()`, and the hook the
 * HTTP transports use to stash it, `transport.setProtocolVersion`, is not
 * implemented by `InMemoryTransport`. The negotiated version exists only in the
 * initialize response, so reading the wire is how you see it.
 *
 * `onmessage` has to be intercepted through an accessor rather than wrapped:
 * `client.connect()` takes ownership of the transport and overwrites the
 * callbacks that are already on it, so anything assigned beforehand is thrown
 * away. Defining the property means the overwrite goes through the setter.
 */
export function recordWire(transport) {
  const wire = [];

  const send = transport.send.bind(transport);
  transport.send = async (message, options) => {
    wire.push({ direction: "out", message });
    return send(message, options);
  };

  let handler;
  Object.defineProperty(transport, "onmessage", {
    configurable: true,
    get: () => handler,
    set: (fn) => {
      handler = (message, extra) => {
        wire.push({ direction: "in", message });
        return fn(message, extra);
      };
    },
  });

  return wire;
}

/**
 * Connects a client to a server over a linked pair, server side first.
 *
 * The order is not stylistic. `client.connect()` sends initialize and waits for
 * the response; if nothing is listening on the other half the request is parked
 * on that half's queue and nothing reports that the peer is missing. The wait
 * is not infinite — initialize is an ordinary request and carries the SDK's
 * default 60 second timeout — but a suite that connects the client too early
 * stalls for a minute and then fails with `Request timed out`, which names
 * neither the transport nor the missing server. Connecting the server first, or
 * starting both and awaiting after, is what avoids it. The contract measures
 * the queueing, the stall, and the timeout.
 */
export async function connectInMemory(server, clientInfo = { name: "units-test-client", version: "0.0.0" }) {
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  const wire = recordWire(clientTransport);
  const client = new Client(clientInfo);

  await server.connect(serverTransport);
  await client.connect(clientTransport);

  return { client, clientTransport, serverTransport, wire };
}

/**
 * Drives the raw JSON-RPC side of the pair with no `Client` involved, which is
 * the only way to ask the server for a protocol version the SDK's own client
 * would never request.
 */
export async function rawExchange(server, request) {
  const [rawTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  const replies = [];
  rawTransport.onmessage = (message) => replies.push(message);

  await server.connect(serverTransport);
  await rawTransport.start();
  await rawTransport.send(request);
  // Delivery is a direct call into the peer's `onmessage`, so the reply lands
  // as soon as the handler's promise chain drains. One macrotask is plenty.
  await new Promise((resolve) => setTimeout(resolve, 0));

  return replies;
}
