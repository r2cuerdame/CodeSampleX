import { WebSocketServer, WebSocket } from 'ws';

// ws is CommonJS but ships named exports that ESM can import directly.
// Messages arrive as Buffer by default, so compare with String(data) rather
// than === on the raw value.
export function startEchoServer() {
  const server = new WebSocketServer({ port: 0, host: '127.0.0.1' });
  server.on('connection', (socket) => {
    socket.on('message', (data) => socket.send(`echo:${String(data)}`));
  });
  return new Promise((resolve) => server.on('listening', () => resolve(server)));
}

export function askEcho(port, message) {
  return new Promise((resolve, reject) => {
    const client = new WebSocket(`ws://127.0.0.1:${port}`);
    client.on('open', () => client.send(message));
    client.on('message', (data) => {
      client.close();
      resolve(String(data));
    });
    client.on('error', reject);
  });
}
