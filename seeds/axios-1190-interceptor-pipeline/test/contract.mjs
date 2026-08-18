import assert from 'node:assert/strict';
import axios from 'axios';

function offlineAdapter(trace, label = 'adapter') {
  return config => {
    trace.push(label);
    return Promise.resolve({
      data: { ok: true },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
      request: { offline: true }
    });
  };
}

{
  const trace = [];
  const client = axios.create({ adapter: offlineAdapter(trace) });
  client.interceptors.request.use(config => {
    trace.push('request:first');
    config.headers.set('X-Order', config.headers.get('X-Order') + '>first');
    return config;
  }, undefined, { synchronous: true });
  client.interceptors.request.use(config => {
    trace.push('request:second');
    config.headers.set('X-Order', 'second');
    return config;
  }, undefined, { synchronous: true });
  client.interceptors.response.use(response => {
    trace.push('response:first');
    response.data.order = ['first'];
    return response;
  });
  client.interceptors.response.use(response => {
    trace.push('response:second');
    response.data.order.push('second');
    return response;
  });

  const pending = client.get('https://example.com/ordered');
  assert.deepEqual(trace, ['request:second', 'request:first', 'adapter']);
  const response = await pending;
  assert.deepEqual(trace, [
    'request:second',
    'request:first',
    'adapter',
    'response:first',
    'response:second'
  ]);
  assert.equal(response.config.headers.get('X-Order'), 'second>first');
  assert.deepEqual(response.data.order, ['first', 'second']);
}

{
  const trace = [];
  const client = axios.create({ adapter: offlineAdapter(trace) });
  client.interceptors.request.use(config => {
    trace.push('sync:ran');
    return config;
  }, undefined, { synchronous: true });
  client.interceptors.request.use(async config => {
    trace.push('async:must-not-run');
    return config;
  }, undefined, {
    synchronous: false,
    runWhen(config) {
      trace.push(`runWhen:${config.method}`);
      return false;
    }
  });

  const pending = client.get('https://example.com/skipped');
  assert.deepEqual(trace, ['runWhen:get', 'sync:ran', 'adapter']);
  await pending;
  assert.deepEqual(trace, ['runWhen:get', 'sync:ran', 'adapter']);
}

{
  const trace = [];
  const client = axios.create({ adapter: offlineAdapter(trace) });
  client.interceptors.request.use(async config => {
    trace.push('async:ran');
    return config;
  });

  const pending = client.get('https://example.com/async');
  assert.deepEqual(trace, []);
  await pending;
  assert.deepEqual(trace, ['async:ran', 'adapter']);
}

{
  const trace = [];
  const client = axios.create({ adapter: offlineAdapter(trace) });
  const removed = client.interceptors.request.use(config => {
    trace.push('ejected');
    return config;
  }, undefined, { synchronous: true });
  client.interceptors.request.eject(removed);
  client.interceptors.request.use(config => {
    trace.push('kept');
    return config;
  }, undefined, { synchronous: true });

  await client.get('https://example.com/after-eject');
  assert.deepEqual(trace, ['kept', 'adapter']);

  trace.length = 0;
  client.interceptors.request.clear();
  await client.get('https://example.com/after-clear');
  assert.deepEqual(trace, ['adapter']);
}

{
  const trace = [];
  const client = axios.create({ adapter: offlineAdapter(trace) });
  client.interceptors.request.use(
    config => {
      trace.push('older:fulfilled');
      return config;
    },
    error => {
      trace.push('older:recovered');
      return error.config;
    }
  );
  client.interceptors.request.use(config => {
    trace.push('newer:throws');
    const error = new Error('request interceptor failed');
    error.config = config;
    throw error;
  });

  const response = await client.get('https://example.com/recovered');
  assert.equal(response.status, 200);
  assert.deepEqual(trace, ['newer:throws', 'older:recovered', 'adapter']);
}

console.log('CONTRACT PASS: Axios 1.19.0 interceptor ordering and controls are measured');
