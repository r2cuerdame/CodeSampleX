import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { dirname, extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';
import puppeteer from 'puppeteer';

const root = normalize(join(dirname(fileURLToPath(import.meta.url)), '..'));

const html = `<!doctype html>
<html><head>
  <meta charset="utf-8">
  <script type="importmap">{
    "imports": {
      "three": "/node_modules/three/build/three.module.js",
      "three/addons/": "/node_modules/three/examples/jsm/"
    }
  }</script>
</head><body><canvas id="canvas" width="2" height="2"></canvas>
<script type="module">
try {
  const THREE = await import('three');
  const { OutputPass } = await import('three/addons/postprocessing/OutputPass.js');

  const canvas = document.querySelector('#canvas');
  const context = canvas.getContext('webgl2', { antialias: false });
  if (!context) throw new Error('Chrome did not provide WebGL 2');

  const renderer = new THREE.WebGLRenderer({ canvas, context });
  renderer.setSize(2, 2, false);
  const readBuffer = new THREE.WebGLRenderTarget(2, 2);
  const writeBuffer = new THREE.WebGLRenderTarget(2, 2);
  const output = new OutputPass();
  output.renderToScreen = true;

  const initialVersion = output.material.version;
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  renderer.toneMappingExposure = 2.25;
  output.render(renderer, writeBuffer, readBuffer);
  const afterFirstRender = output.material.version;
  const firstDefines = Object.keys(output.material.defines).sort();

  renderer.toneMappingExposure = 0.75;
  output.render(renderer, writeBuffer, readBuffer);
  const afterExposureOnly = output.material.version;

  renderer.toneMapping = THREE.NeutralToneMapping;
  output.render(renderer, writeBuffer, readBuffer);
  const afterToneMappingChange = output.material.version;
  const neutralDefines = Object.keys(output.material.defines).sort();

  renderer.outputColorSpace = THREE.LinearSRGBColorSpace;
  output.render(renderer, writeBuffer, readBuffer);
  const afterColorSpaceChange = output.material.version;
  const linearDefines = Object.keys(output.material.defines).sort();

  window.__contractResult = {
    webglVersion: context.getParameter(context.VERSION),
    initialVersion,
    afterFirstRender,
    afterExposureOnly,
    afterToneMappingChange,
    afterColorSpaceChange,
    firstDefines,
    neutralDefines,
    linearDefines,
    exposureUniform: output.uniforms.toneMappingExposure.value,
    inputTextureIdentity: output.uniforms.tDiffuse.value === readBuffer.texture,
    isOutputPass: output.isOutputPass
  };

  output.dispose();
  readBuffer.dispose();
  writeBuffer.dispose();
  renderer.dispose();
} catch (error) {
  window.__contractResult = { error: String(error?.stack || error) };
}
</script></body></html>`;

const contentTypes = new Map([
  ['.js', 'text/javascript; charset=utf-8'],
  ['.html', 'text/html; charset=utf-8']
]);

const server = createServer(async (request, response) => {
  try {
    if (request.url === '/') {
      response.writeHead(200, { 'content-type': contentTypes.get('.html') });
      response.end(html);
      return;
    }
    const requested = normalize(join(root, decodeURIComponent(request.url)));
    if (!requested.startsWith(root + '\\') && !requested.startsWith(root + '/')) {
      response.writeHead(403).end();
      return;
    }
    const body = await readFile(requested);
    response.writeHead(200, {
      'content-type': contentTypes.get(extname(requested)) || 'application/octet-stream'
    });
    response.end(body);
  } catch {
    response.writeHead(404).end();
  }
});

await new Promise((resolve, reject) => {
  server.once('error', reject);
  server.listen(0, '127.0.0.1', resolve);
});

const browser = await puppeteer.launch({
  headless: true,
  args: [
    '--no-sandbox',
    '--disable-setuid-sandbox',
    '--enable-webgl',
    '--enable-unsafe-swiftshader',
    '--use-angle=swiftshader'
  ]
});

try {
  assert.match(await browser.version(), /^Chrome\/134\./);
  const page = await browser.newPage();
  const address = server.address();
  await page.goto(`http://127.0.0.1:${address.port}/`, { waitUntil: 'load' });
  await page.waitForFunction(() => window.__contractResult !== undefined);
  const result = await page.evaluate(() => window.__contractResult);

  assert.equal(result.error, undefined, result.error);
  assert.match(result.webglVersion, /WebGL 2\.0/);
  assert.equal(result.isOutputPass, true);
  assert.equal(result.inputTextureIdentity, true);

  assert.ok(result.afterFirstRender > result.initialVersion);
  assert.deepEqual(result.firstDefines, ['ACES_FILMIC_TONE_MAPPING', 'SRGB_TRANSFER']);

  assert.equal(result.afterExposureOnly, result.afterFirstRender);
  assert.equal(result.exposureUniform, 0.75);

  assert.ok(result.afterToneMappingChange > result.afterExposureOnly);
  assert.deepEqual(result.neutralDefines, ['NEUTRAL_TONE_MAPPING', 'SRGB_TRANSFER']);

  assert.ok(result.afterColorSpaceChange > result.afterToneMappingChange);
  assert.deepEqual(result.linearDefines, ['NEUTRAL_TONE_MAPPING']);

  console.log('CONTRACT PASS: three r185 OutputPass tracked renderer state in offline Chrome 134');
} finally {
  await browser.close();
  await new Promise(resolve => server.close(resolve));
}
