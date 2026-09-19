import http from 'node:http';
import { createHash } from 'node:crypto';
import { strict as assert } from 'node:assert';
import { WebSocket } from 'undici';
// Claim (8.10.2): when a server's 101 selects a subprotocol the client never requested, the
// client fails the connection (internally with protocol error 1002) instead of throwing an
// uncaught TypeError that terminates the process. The 1002 never reaches the API or the wire:
// the close event reports 1006 and no close frame is sent, so the contract asserts the failure
// itself and records what the wire saw.
let uncaught;
const record = (e) => { uncaught = e; };
process.on('uncaughtException', record);
let wireClose;
const server = http.createServer();
server.on('upgrade', (req, socket) => {
  const accept = createHash('sha1').update(req.headers['sec-websocket-key'] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
  socket.on('data', (buf) => {
    // A masked close frame from the client: opcode 0x8, then the 2-byte status code under the mask.
    if ((buf[0] & 0x0f) === 0x8 && (buf[1] & 0x80) && (buf[1] & 0x7f) >= 2) {
      const mask = buf.subarray(2, 6);
      wireClose = ((buf[6] ^ mask[0]) << 8) | (buf[7] ^ mask[1]);
    }
  });
  socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n' +
    `Sec-WebSocket-Accept: ${accept}\r\nSec-WebSocket-Protocol: unrequested\r\n\r\n`);
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const ws = new WebSocket(`ws://127.0.0.1:${server.address().port}/`);
const outcome = await new Promise((resolve) => {
  ws.addEventListener('open', () => resolve('open'));
  ws.addEventListener('close', () => resolve('close'));
  ws.addEventListener('error', () => {});
  setTimeout(() => resolve('timeout'), 3000);
});
await new Promise((r) => setTimeout(r, 100));
process.off('uncaughtException', record);
server.closeAllConnections();
server.close();
try {
  assert.equal(uncaught, undefined, `uncaught exception during the handshake: ${uncaught}`);
  assert.equal(outcome, 'close', `handshake ended with ${outcome}, not close`);
  assert.equal(ws.readyState, WebSocket.CLOSED);
} catch (e) {
  console.error(e.message);
  process.exit(1);
}
console.log(`CONTRACT PASS (close frame on the wire: ${wireClose})`);
process.exit(0);
