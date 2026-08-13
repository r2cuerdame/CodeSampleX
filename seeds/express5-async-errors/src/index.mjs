import express from 'express';

// express 5 forwards a rejected promise from an async handler to the error
// middleware by itself. Under express 4 the same handler hung until the
// client timed out unless every route was wrapped in a try/catch or an
// asyncHandler helper — those wrappers are now unnecessary.
export function createApp() {
  const app = express();

  app.get('/boom', async () => {
    throw new Error('async failure');
  });

  // Four arguments: express identifies error middleware by arity, so
  // dropping the unused `next` turns this back into an ordinary handler.
  app.use((err, _req, res, _next) => {
    res.status(500).json({ handled: true, message: err.message });
  });

  return app;
}
