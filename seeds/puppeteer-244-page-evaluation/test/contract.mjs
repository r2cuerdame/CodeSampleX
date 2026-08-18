import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import puppeteer from 'puppeteer';


const packageMetadata = JSON.parse(
  await readFile(new URL('../node_modules/puppeteer/package.json', import.meta.url), 'utf8')
);
assert.equal(packageMetadata.version, '24.4.0');

const browser = await puppeteer.launch({
  headless: true,
  args: ['--no-sandbox', '--disable-setuid-sandbox']
});

try {
  assert.match(await browser.version(), /^Chrome\/134\./);
  const page = await browser.newPage();
  await page.setContent(`<!doctype html>
    <html><body>
      <ul><li class="item">alpha</li><li class="item">beta</li></ul>
    </body></html>`);

  const mistakenSingleElement = await page.$$eval('.item', element => element.textContent);
  assert.equal(mistakenSingleElement, undefined);

  const itemTexts = await page.$$eval(
    '.item',
    elements => elements.map(element => element.textContent)
  );
  assert.deepEqual(itemTexts, ['alpha', 'beta']);

  await assert.rejects(
    () => page.$eval('.missing', element => element.textContent),
    /failed to find element matching selector/
  );
  const missingCount = await page.$$eval('.missing', elements => elements.length);
  assert.equal(missingCount, 0);

  const serializedBody = await page.evaluate(() => document.body);
  assert.deepEqual(serializedBody, {});

  await page.exposeFunction('syncAdd', (left, right) => left + right);
  const exposedShape = await page.evaluate(() => {
    const result = window.syncAdd(2, 3);
    return {
      isPromise: result instanceof Promise,
      stringValue: String(result)
    };
  });
  assert.deepEqual(exposedShape, {
    isPromise: true,
    stringValue: '[object Promise]'
  });
  assert.equal(await page.evaluate(() => window.syncAdd(2, 3)), 5);

  console.log(
    'CONTRACT PASS: Puppeteer 24.4.0 page evaluation APIs ran in offline Chrome 134'
  );
} finally {
  await browser.close();
}
