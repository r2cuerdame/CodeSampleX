import { Chalk } from 'chalk';

// chalk auto-detects colour support and turns itself OFF when stdout is not
// a TTY — CI logs, pipes and captured output all fall into that case, so the
// usual "chalk prints no colours" report is not a bug. An explicit Chalk
// instance keeps the decision in your code instead of the environment.
export const forced = new Chalk({ level: 1 });
export const plain = new Chalk({ level: 0 });

export function warn(text) {
  return forced.red(text);
}
