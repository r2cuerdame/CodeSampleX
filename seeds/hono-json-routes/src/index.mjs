import { Hono } from 'hono';

// Hono handlers return a Response; there is no res object and no next()
// for the normal path. app.fetch is a plain (Request) => Response, which
// is why the same app runs on Node, workers and Deno unchanged — and why
// it can be tested with no server listening at all.
export function createApp() {
  const app = new Hono();
  app.get('/items/:id', (c) => c.json({ id: c.req.param('id'), ok: true }));
  app.post('/items', async (c) => {
    const body = await c.req.json();
    return c.json({ created: body }, 201);
  });
  return app;
}
