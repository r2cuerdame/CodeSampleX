import pLimit from 'p-limit';

// pLimit returns a function that queues calls past the limit. The tasks must
// be wrapped BEFORE Promise.all sees them: Promise.all(tasks.map(t => t()))
// starts everything at once and the limiter never applies.
export async function runLimited(tasks, concurrency) {
  const limit = pLimit(concurrency);
  return Promise.all(tasks.map((task) => limit(task)));
}
