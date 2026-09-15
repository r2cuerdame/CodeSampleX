// Regression test suite for PurplePulse client telemetry.
const fs = require('fs');
const assert = require('assert');
const vm = require('vm');

const scriptCode = fs.readFileSync(__dirname + '/static/pulse.js', 'utf8');
if (!scriptCode || scriptCode.length === 0) {
  throw new Error('static/pulse.js not found or empty');
}

function createMockEnvironment(options = {}) {
  const storage = options.storage || {};
  let cookieStr = options.cookies || '';
  const fetchCalls = [];

  const localStorage = options.throwStorage ? {
    getItem() { throw new Error('Storage disabled'); },
    setItem() { throw new Error('Storage disabled'); }
  } : {
    getItem(k) { return Object.prototype.hasOwnProperty.call(storage, k) ? storage[k] : null; },
    setItem(k, v) { storage[k] = String(v); },
    _data: storage
  };

  const listeners = {};
  const document = {
    get cookie() { return cookieStr; },
    set cookie(val) {
      const parts = val.split(';');
      const pair = parts[0].trim();
      const eqIdx = pair.indexOf('=');
      const k = pair.slice(0, eqIdx);
      const v = pair.slice(eqIdx + 1);
      const existing = cookieStr.split(';').map(s => s.trim()).filter(Boolean);
      const updated = existing.filter(s => !s.startsWith(k + '='));
      updated.push(k + '=' + v);
      cookieStr = updated.join('; ');
    },
    getElementById(id) {
      if (id === 'purple-pulse') {
        return {
          getAttribute(attr) {
            if (attr === 'data-version') return options.version !== undefined ? options.version : 'v1.0.0';
            if (attr === 'data-env') return options.env !== undefined ? options.env : 'production';
            if (attr === 'data-project-id') return 'pp_codesamplex_f2f2ab10';
            return null;
          }
        };
      }
      return null;
    },
    currentScript: null,
    addEventListener(type, fn, opts) {
      if (!listeners[type]) listeners[type] = [];
      listeners[type].push({ fn, opts });
    },
    removeEventListener(type, fn) {
      if (listeners[type]) {
        listeners[type] = listeners[type].filter(l => l.fn !== fn);
      }
    },
    dispatchEvent(event) {
      const type = event.type;
      if (listeners[type]) {
        const handlers = listeners[type].slice();
        handlers.forEach(l => {
          l.fn(event);
          if (l.opts && l.opts.once) {
            document.removeEventListener(type, l.fn);
          }
        });
      }
    }
  };

  const window = {
    location: {
      hostname: options.hostname || 'codesamplex.dev'
    },
    navigator: {
      webdriver: !!options.webdriver,
      userAgent: options.userAgent || 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36',
      platform: options.platform !== undefined ? options.platform : 'Win32'
    },
    localStorage: localStorage,
    document: document,
    fetch: function(url, opts) {
      fetchCalls.push({ url, opts, body: JSON.parse(opts.body) });
      if (options.fetchFail) {
        return Promise.reject(new Error('Network error'));
      }
      const status = options.fetchStatus !== undefined ? options.fetchStatus : 202;
      return Promise.resolve({
        status: status,
        ok: status >= 200 && status < 300,
        json: () => Promise.resolve({ accepted: true })
      });
    },
    AbortSignal: typeof AbortSignal !== 'undefined' ? AbortSignal : undefined,
    AbortController: typeof AbortController !== 'undefined' ? AbortController : undefined,
    setTimeout: setTimeout,
    clearTimeout: clearTimeout,
    Date: Date,
    Math: Math,
    crypto: {
      randomUUID: () => '11111111-2222-4333-8444-555555555555'
    }
  };

  return { window, document, localStorage, fetchCalls, storage };
}

async function runScriptAsync(env, interaction = { type: 'pointerdown', isTrusted: true }) {
  const context = vm.createContext(Object.assign({}, env.window, {
    window: env.window,
    document: env.document,
    localStorage: env.localStorage,
    navigator: env.window.navigator,
    location: env.window.location,
    fetch: env.window.fetch,
    Date: Date,
    Math: Math,
    setTimeout: setTimeout,
    clearTimeout: clearTimeout,
    encodeURIComponent: encodeURIComponent,
    decodeURIComponent: decodeURIComponent
  }));
  vm.runInContext(scriptCode, context);
  // Wait for promise microtasks/event loop to settle
  await new Promise(resolve => setTimeout(resolve, 20));
  
  // Save pre-interaction fetch count
  env.preInteractionFetchCount = env.fetchCalls.length;
  
  // Trigger interaction
  if (env.document && env.document.dispatchEvent) {
      env.document.dispatchEvent(interaction);
      await new Promise(resolve => setTimeout(resolve, 20));
  }
}

(async function() {
  // Test 1: First visit on production host with 202 response
  console.log('Running Test 1: First visit on production host...');
  {
    const env = createMockEnvironment({
      hostname: 'codesamplex.dev',
      version: 'v0.1.44-66',
      env: 'production',
      fetchStatus: 202
    });
    await runScriptAsync(env);
    assert.strictEqual(env.preInteractionFetchCount, 0, 'Passive load must send nothing');

    assert.strictEqual(env.fetchCalls.length, 1, 'Should make exactly 1 fetch call');
    const call = env.fetchCalls[0];
    assert.strictEqual(call.url, 'https://pulse-api.purpleshiphub.workers.dev/api/v1/ping');
    assert.strictEqual(call.opts.method, 'POST');
    assert.strictEqual(call.opts.headers['Content-Type'], 'application/json');
    assert.strictEqual(call.opts.mode, 'cors');
    assert.strictEqual(call.opts.credentials, 'omit');
    assert.strictEqual(call.opts.keepalive, true);

    const payload = call.body;
    assert.strictEqual(payload.project_id, 'pp_codesamplex_f2f2ab10');
    assert.strictEqual(payload.install_id, '11111111-2222-4333-8444-555555555555');
    assert.strictEqual(payload.version, 'v0.1.44-66');
    assert.strictEqual(payload.platform, 'web');
    assert.strictEqual(payload.os, 'windows');
    assert.strictEqual(payload.schema_version, 2);
    assert.strictEqual(payload.browser, undefined);
    assert.strictEqual(payload.device_type, undefined);
    assert.strictEqual(payload.webview, undefined);
    assert.strictEqual(payload.environment, undefined, 'environment must be omitted in production');

    // Verify storage marked with pp_last_attempt
    assert.strictEqual(env.storage.pp_install_id, '11111111-2222-4333-8444-555555555555');
    assert.ok(env.storage.pp_last_attempt, 'pp_last_attempt must be recorded');
    assert.ok(env.document.cookie.includes('pp_install_id='), 'cookie must have pp_install_id');
    assert.ok(env.document.cookie.includes('pp_last_attempt='), 'cookie must have pp_last_attempt');

    // Test 2: Second visit on same local day -> NO duplicate attempt
    console.log('Running Test 2: Second visit on same local day...');
    await runScriptAsync(env);
    assert.strictEqual(env.fetchCalls.length, 1, 'Should NOT make a second fetch call on same day');
  }

  // Test 2b: Synthetic script-dispatched interaction must not count as a human in prod
  console.log('Running Test 2b: Synthetic interaction is ignored in production...');
  {
    const env = createMockEnvironment({
      hostname: 'codesamplex.dev',
      env: 'production'
    });
    await runScriptAsync(env, { type: 'pointerdown', isTrusted: false });
    assert.strictEqual(env.fetchCalls.length, 0, 'Synthetic interaction must NOT send prod telemetry');
    assert.strictEqual(env.storage.pp_install_id, undefined, 'Synthetic interaction must not create an install ID');
    assert.strictEqual(env.storage.pp_last_attempt, undefined, 'Synthetic interaction must not consume the daily attempt');
  }
  // Test 3: Network failure still records pp_last_attempt immediately, suppressing retry storms
  console.log('Running Test 3: Network failure records pp_last_attempt and suppresses retry storms...');
  {
    const env = createMockEnvironment({
      hostname: 'codesamplex.dev',
      env: 'production',
      fetchFail: true
    });
    await runScriptAsync(env);
    assert.strictEqual(env.fetchCalls.length, 1);
    assert.ok(env.storage.pp_last_attempt, 'Must mark pp_last_attempt before fetch to prevent retry storms');

    // Next page load that day must NOT retry
    await runScriptAsync(env);
    assert.strictEqual(env.fetchCalls.length, 1, 'Subsequent page load on same day must not retry after failure');
  }

  // Test 4: Non-production environment whitelist ('test' and 'dev')
  console.log('Running Test 4: Non-production environment whitelist...');
  {
    // 'test' is allowed
    const envTest = createMockEnvironment({
      hostname: 'codesamplex.dev',
      version: 'v0.1.0',
      env: 'test'
    });
    await runScriptAsync(envTest);
    assert.strictEqual(envTest.fetchCalls.length, 1);
    assert.strictEqual(envTest.fetchCalls[0].body.environment, 'test');

    // 'dev' is allowed
    const envDev = createMockEnvironment({
      hostname: 'codesamplex.dev',
      version: 'v0.1.0',
      env: 'dev'
    });
    await runScriptAsync(envDev);
    assert.strictEqual(envDev.fetchCalls.length, 1);
    assert.strictEqual(envDev.fetchCalls[0].body.environment, 'dev');

    // 'staging', 'unknown', 'local', 'development' must NOT send
    for (const disallowed of ['staging', 'unknown', 'local', 'development', 'custom']) {
      const envDisallowed = createMockEnvironment({
        hostname: 'codesamplex.dev',
        version: 'v0.1.0',
        env: disallowed
      });
      await runScriptAsync(envDisallowed);
      assert.strictEqual(envDisallowed.fetchCalls.length, 0, `Environment '${disallowed}' must NOT send telemetry`);
    }
  }

  // Test 5: Guard against sending prod telemetry during automated tests (navigator.webdriver = true)
  console.log('Running Test 5: Guard against sending prod telemetry when navigator.webdriver is true...');
  {
    const env = createMockEnvironment({
      hostname: 'codesamplex.dev',
      env: 'production',
      webdriver: true
    });
    await runScriptAsync(env);
    assert.strictEqual(env.fetchCalls.length, 0, 'Must NOT send prod telemetry when navigator.webdriver is true');
  }

  // Test 6: Guard against sending prod telemetry from local test host (127.0.0.1)
  console.log('Running Test 6: Guard against sending prod telemetry from localhost/127.0.0.1...');
  {
    const env = createMockEnvironment({
      hostname: '127.0.0.1',
      env: 'production',
      webdriver: false
    });
    await runScriptAsync(env);
    assert.strictEqual(env.fetchCalls.length, 0, 'Must NOT send prod telemetry from 127.0.0.1');
  }

  // Test 7: Fallback to cookie when localStorage is disabled or throws
  console.log('Running Test 7: Fallback to cookie when localStorage throws...');
  {
    const env = createMockEnvironment({
      hostname: 'codesamplex.dev',
      env: 'production',
      throwStorage: true,
      cookies: 'pp_install_id=37266039-77aa-4dbd-a3fb-7ca31984ff65'
    });
    await runScriptAsync(env);
    assert.strictEqual(env.fetchCalls.length, 1, 'Should succeed with cookie fallback');
    assert.strictEqual(env.fetchCalls[0].body.install_id, '37266039-77aa-4dbd-a3fb-7ca31984ff65');
  }

  // Test 8: OS detection exact vocabulary: windows|android|ios|macos|linux|other
  console.log('Running Test 8: OS detection exact vocabulary...');
  {
    const cases = [
      // Windows
      ['Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36', 'Win32', 'windows'],
      // Android (must detect before Linux)
      ['Mozilla/5.0 (Linux; Android 13; SM-S901B) AppleWebKit/537.36 Chrome/112.0.0.0 Mobile Safari/537.36', 'Linux armv8l', 'android'],
      // iOS (must detect before macOS)
      ['Mozilla/5.0 (iPhone; CPU iPhone OS 16_5 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1', 'iPhone', 'ios'],
      ['Mozilla/5.0 (iPad; CPU OS 16_5 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1', 'iPad', 'ios'],
      // macOS (mapped to macos, not mac)
      ['Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36', 'MacIntel', 'macos'],
      // Generic Linux
      ['Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/119.0', 'Linux x86_64', 'linux'],
      // Unknown UA mapped to other
      ['CustomBot/1.0', '', 'other']
    ];

    for (const [ua, plat, expected] of cases) {
      const env = createMockEnvironment({
        hostname: 'codesamplex.dev',
        env: 'test',
        userAgent: ua,
        platform: plat,
        fetchStatus: 200
      });
      await runScriptAsync(env);
      assert.strictEqual(env.fetchCalls[0].body.os, expected, `UA ${ua} with plat ${plat} should map to ${expected}`);
    }
  }

  console.log('All PurplePulse JavaScript tests PASSED successfully!');
})();
