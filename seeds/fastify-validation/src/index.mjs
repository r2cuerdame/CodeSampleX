import Fastify from 'fastify';

// The schema is not documentation: fastify compiles it and rejects a bad
// body with 400 BEFORE the handler runs, so the handler never needs to
// re-check. app.inject drives the whole stack in-process, which is why
// these tests need no port and no teardown.
export function createApp() {
  const app = Fastify();
  app.post('/peers', {
    schema: {
      body: {
        type: 'object',
        required: ['peerId', 'port'],
        properties: {
          peerId: { type: 'string', minLength: 3 },
          port: { type: 'integer', minimum: 1, maximum: 65535 },
        },
      },
    },
  }, async (req) => ({ registered: true, peerId: req.body.peerId }));
  return app;
}
