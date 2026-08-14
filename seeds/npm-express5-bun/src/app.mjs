import express from "express";

/**
 * Express 5 on whichever runtime this sample declares.
 *
 * The interesting part is that the file is identical in both: a route with
 * a parameter, a JSON body parser, a handler that throws, and a request for
 * a path that does not exist. Those four are where a Node-compatibility
 * layer usually diverges, because they lean on node:http semantics rather
 * than on Express itself — the thrown handler in particular has to travel
 * through Express's error handling and come back as a 500 rather than
 * killing the process.
 */
export function buildApp() {
  const app = express();
  app.use(express.json());
  app.get("/items/:id", (req, res) => res.json({ id: req.params.id, q: req.query }));
  app.post("/echo", (req, res) => res.status(201).json(req.body));
  app.get("/boom", () => {
    throw new Error("kaboom");
  });
  return app;
}

/** Listens on an ephemeral loopback port and resolves the base URL. */
export async function listen(app) {
  const server = app.listen(0, "127.0.0.1");
  await new Promise((resolve) => server.once("listening", resolve));
  return { server, base: `http://127.0.0.1:${server.address().port}` };
}
