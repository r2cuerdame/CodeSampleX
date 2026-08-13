import { strict as assert } from 'node:assert';
import { fits, majorOfTag } from '../src/index.mjs';

assert.equal(fits('1.12.0', '^1.0.0'), true);
assert.equal(fits('2.0.0', '^1.0.0'), false);

// The prerelease trap: excluded by default, included on request.
assert.equal(fits('1.13.0-beta.1', '^1.0.0'), false);
assert.equal(fits('1.13.0-beta.1', '^1.0.0', true), true);

assert.equal(majorOfTag('v3.2.1'), 3);
assert.equal(majorOfTag('not-a-version'), null);
console.log('CONTRACT PASS: semver range checks behaved as documented');
