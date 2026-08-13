import express from 'express';

// express 5 still needs express.json() mounted before a JSON route:
// without it req.body is undefined, which is the classic first bug.
export function createApp() {
  const app = express();
  app.use(express.json());
  app.post('/items', (req, res) => {
    res.status(201).json({ created: req.body });
  });
  return app;
}
