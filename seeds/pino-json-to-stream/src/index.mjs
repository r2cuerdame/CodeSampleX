import pino from 'pino';

// pino's second argument is the destination; anything with write(string)
// works, which is how you capture output in tests without a transport.
// base: null drops the default pid/hostname pair. Each log call emits one
// newline-delimited JSON object — never pretty text unless you add a
// transport, so parsing the raw line is the reliable way to assert on it.
export function loggerTo(stream) {
  return pino({ base: null }, stream);
}
