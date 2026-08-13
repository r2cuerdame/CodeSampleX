// Prints one sentinel-prefixed JSON line so the contract can read a result
// even when dotenv writes its own banner to stdout.
export function report(name, apiBase) {
  process.stdout.write(
    'CSX_RESULT ' +
      JSON.stringify({
        scenario: name,
        // typeof survives the trip: JSON has no `undefined`.
        capturedType: typeof apiBase,
        captured: apiBase ?? null,
        // Proof that the .env itself was found and parsed: by the time this
        // line runs, process.env IS populated. Only the module-scope capture
        // above happened too early.
        envNow: process.env.CSX_DEMO_API_BASE ?? null,
      }) +
      '\n',
  );
}
