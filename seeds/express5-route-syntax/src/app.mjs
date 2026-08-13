import express from 'express';
import { overwriteQuery, mutateQuery } from './v4-habits.mjs';

function attempt(fn) {
  try {
    fn();
    return null;
  } catch (err) {
    return err;
  }
}

// The express 5 spellings of the three express 4 routing habits.
export function createApp() {
  const app = express();

  // v4: app.get('/users/:id?', ...)
  // v5: an optional part is a braced group — and the SLASH belongs inside the
  // braces. '/users/{:id}' compiles too, but it only matches '/users/' and
  // 404s on a bare '/users', which is rarely what ':id?' meant.
  app.get('/users{/:id}', (req, res) => {
    res.json({ route: 'users', id: req.params.id ?? null });
  });

  // v4: app.get('/files/*', ...) then read req.params[0] as a string.
  // v5: every wildcard needs a name, and the capture is an ARRAY of decoded
  // path segments — join it to get the old string back.
  app.get('/files/*splat', (req, res) => {
    res.json({ route: 'files', path: req.params.splat.join('/'), segments: req.params.splat });
  });

  // v4: middleware normalized req.query in place.
  // v5: req.query is a getter — copy it and pass the copy along.
  app.get('/search', (req, res) => {
    const assignError = attempt(() => overwriteQuery(req));
    const mutateError = attempt(() => mutateQuery(req));
    const normalized = { ...req.query, page: Number(req.query.page ?? 1) };
    res.json({
      assignMessage: assignError?.message ?? null,
      mutateMessage: mutateError?.message ?? null,
      writeSurvived: req.query.normalized ?? null,
      normalized,
    });
  });

  return app;
}
