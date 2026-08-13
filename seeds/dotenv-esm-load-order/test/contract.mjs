import { strict as assert } from 'node:assert';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import fs from 'node:fs';
import path from 'node:path';

const root = fileURLToPath(new URL('..', import.meta.url));
const envPath = path.join(root, '.env');
const KEY = 'CSX_DEMO_API_BASE';
const VALUE = 'https://api.example.test/v1';

// The .env is created here and removed in the finally block below, so the
// sample never ships one. Its only key is a fake URL — nothing secret.
const ENV_FILE = `# Written by test/contract.mjs and deleted when it finishes.\n${KEY}=${VALUE}\n`;

// Each scenario must start from an environment where the key is genuinely
// unset, otherwise "broken" could accidentally pass by inheriting it.
const childEnv = { ...process.env };
delete childEnv[KEY];
assert.equal(process.env[KEY], undefined, 'precondition: key must not be preset');

function run(script) {
  const r = spawnSync(process.execPath, [path.join('src', script)], {
    cwd: root, // dotenv resolves .env against process.cwd()
    env: childEnv,
    encoding: 'utf8',
  });
  assert.equal(r.status, 0, `${script} exited ${r.status}: ${r.stderr}`);
  // dotenv may print a banner of its own, so pick the sentinel line.
  const line = r.stdout.split('\n').find((l) => l.startsWith('CSX_RESULT '));
  assert.ok(line, `${script} printed no CSX_RESULT line. stdout: ${r.stdout}`);
  return JSON.parse(line.slice('CSX_RESULT '.length));
}

fs.writeFileSync(envPath, ENV_FILE);
let broken, fixed, fixedDynamic;
try {
  broken = run('broken.mjs');
  fixed = run('fixed.mjs');
  fixedDynamic = run('fixed-dynamic.mjs');
} finally {
  fs.rmSync(envPath, { force: true });
}

// 1. The bug: a module that reads process.env at import time captures
//    undefined, because ESM evaluated it before dotenv.config() ran.
assert.equal(broken.capturedType, 'undefined');
assert.equal(broken.captured, null);

// 2. ...and it is purely a load-order problem, not a missing/unreadable .env:
//    the same process sees the value in process.env once config() has run.
assert.equal(broken.envNow, VALUE);

// 3. The fix: `import 'dotenv/config'` placed as the first import populates
//    process.env during the import phase, before ./settings.mjs evaluates.
assert.equal(fixed.capturedType, 'string');
assert.equal(fixed.captured, VALUE);

// 4. The other fix: keep dotenv.config() explicit, then pull the
//    env-reading module in with a dynamic import() that runs after it.
assert.equal(fixedDynamic.capturedType, 'string');
assert.equal(fixedDynamic.captured, VALUE);

// 5. The sample ships no .env.
assert.equal(fs.existsSync(envPath), false, '.env must be cleaned up');

console.log(
  'CONTRACT PASS: import-time env read is undefined under dotenv.config(), ' +
    "and correct via `import 'dotenv/config'` or a dynamic import",
);
