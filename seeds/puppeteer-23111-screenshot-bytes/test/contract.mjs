import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import puppeteer from "puppeteer";

const metadata = JSON.parse(
  await readFile(new URL("../node_modules/puppeteer/package.json", import.meta.url), "utf8"),
);
assert.equal(metadata.version, "23.11.1");

const browser = await puppeteer.launch({
  headless: true,
  args: ["--no-sandbox", "--disable-setuid-sandbox"],
});

try {
  assert.match(await browser.version(), /^Chrome\/134\./);
  const page = await browser.newPage();
  await page.setViewport({ width: 64, height: 32, deviceScaleFactor: 1 });
  await page.setContent(`<!doctype html><style>
    html, body { margin: 0; width: 64px; height: 32px; background: #13579b; }
  </style>`);

  const bytes = await page.screenshot({ type: "png" });
  assert.ok(bytes instanceof Uint8Array);
  assert.equal(Buffer.isBuffer(bytes), false);
  assert.deepEqual([...bytes.subarray(0, 8)], [137, 80, 78, 71, 13, 10, 26, 10]);

  const silentlyWrong = bytes.toString("base64");
  assert.match(silentlyWrong, /^137,80,78,71,13,10,26,10,/);

  const encoded = Buffer.from(bytes).toString("base64");
  assert.match(encoded, /^iVBORw0KGgo/);
  assert.deepEqual(new Uint8Array(Buffer.from(encoded, "base64")), bytes);

  const requestedBase64 = await page.screenshot({ type: "png", encoding: "base64" });
  assert.equal(typeof requestedBase64, "string");
  assert.equal(requestedBase64, encoded);

  console.log("CONTRACT PASS: Puppeteer 23.11.1 screenshot bytes in Chrome 134");
} finally {
  await browser.close();
}
