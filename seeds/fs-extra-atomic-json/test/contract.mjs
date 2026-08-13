import os from 'node:os';
import path from 'node:path';
import fs from 'fs-extra';
import { strict as assert } from 'node:assert';
import { writeConfig, readConfig } from '../src/index.mjs';

const root = await fs.mkdtemp(path.join(os.tmpdir(), 'csx-fsx-'));
const target = path.join(root, 'deep', 'nested', 'config.json');

// The directory does not exist; outputJson creates it.
assert.equal(await fs.pathExists(path.dirname(target)), false);
await writeConfig(target, { name: 'codesamplex', retries: 3 });
assert.equal(await fs.pathExists(target), true);

const back = await readConfig(target);
assert.deepEqual(back, { name: 'codesamplex', retries: 3 });

const copy = path.join(root, 'copy', 'config.json');
await fs.copy(target, copy);
assert.deepEqual(await readConfig(copy), back);

await fs.remove(root);
assert.equal(await fs.pathExists(root), false);

console.log('CONTRACT PASS: fs-extra created parents, round-tripped JSON and cleaned up');
