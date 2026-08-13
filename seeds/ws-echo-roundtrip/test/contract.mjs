import { strict as assert } from 'node:assert';
import { startEchoServer, askEcho } from '../src/index.mjs';

const server = await startEchoServer();
const { port } = server.address();

const reply = await askEcho(port, 'hello');
assert.equal(reply, 'echo:hello');

await new Promise((r) => server.close(r));
console.log('CONTRACT PASS: ws server and client completed an echo round trip');
