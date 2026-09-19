// Issue #228 acceptance: the dependency-health card's first-break
// fingerprint must not widen the page on a phone, must stay whole and wrap,
// and the tables beside it must keep their local horizontal scroll.
import { chromium, devices } from 'playwright';
import fs from 'node:fs';

const BASE = process.env.BASE || 'http://127.0.0.1:8932';
const PATH = '/npm/axios?f_version=2.0.0';
const TOKEN = 'sha256:9c1f7a3e5b2d8046e7a4c0b95d31f8624b8e6d20fa937c150d5837e1b6ac492f';

const targets = [
  { name: 'iPhone 14 (390)',         ctx: devices['iPhone 14'],         mobile: true,  width: 390 },
  { name: 'iPhone 14 Pro Max (430)', ctx: devices['iPhone 14 Pro Max'], mobile: true,  width: 430 },
  { name: 'desktop (1280)',          ctx: { viewport: { width: 1280, height: 900 } }, mobile: false, width: 1280 },
];

const browser = await chromium.launch();
const results = [];
let failed = 0;
for (const t of targets) {
  const context = await browser.newContext(t.ctx);
  const page = await context.newPage();
  await page.goto(BASE + PATH, { waitUntil: 'load' });
  await page.waitForTimeout(300);
  const m = await page.evaluate((TOKEN) => {
    const de = document.documentElement;
    const rect = (el) => { const r = el.getBoundingClientRect(); return { left: Math.round(r.left + scrollX), right: Math.round(r.right + scrollX), width: Math.round(r.width) }; };
    const card = document.querySelector('#dependency-health .dephealth-summary');
    const code = document.querySelector('#dependency-health .break-evidence code');
    const tables = [...document.querySelectorAll('#dependency-health .tablewrap')].map(tw => ({
      ...rect(tw), overflowX: getComputedStyle(tw).overflowX, scrollWidth: tw.scrollWidth, clientWidth: tw.clientWidth,
    }));
    return {
      scrollWidth: de.scrollWidth, clientWidth: de.clientWidth,
      card: card ? { ...rect(card), host: card.parentElement.clientWidth } : null,
      code: code ? { ...rect(code), lines: code.getClientRects().length, text: code.textContent.trim(), full: code.textContent.includes(TOKEN) } : null,
      tables,
    };
  }, TOKEN);
  const shot = `shot-${t.width}.png`;
  await page.screenshot({ path: shot, fullPage: false });
  const errs = [];
  if (m.scrollWidth > m.clientWidth) errs.push(`scrollWidth ${m.scrollWidth} > clientWidth ${m.clientWidth}`);
  if (!m.card) errs.push('no .dephealth-summary rendered');
  else {
    if (m.card.right > m.clientWidth + 0.5) errs.push(`card right ${m.card.right} past ${m.clientWidth}`);
    if (m.card.width > m.card.host + 0.5) errs.push(`card ${m.card.width}px inside ${m.card.host}px section`);
  }
  if (!m.code) errs.push('no fingerprint <code> rendered');
  else {
    if (!m.code.full) errs.push('fingerprint truncated: ' + m.code.text);
    if (m.code.right > m.clientWidth + 0.5) errs.push(`fingerprint right ${m.code.right} past ${m.clientWidth}`);
    if (t.mobile && m.code.lines < 2) errs.push(`fingerprint on ${m.code.lines} line(s) on a phone`);
    if (!t.mobile && m.code.lines !== 1) errs.push(`fingerprint wraps onto ${m.code.lines} lines on desktop`);
  }
  if (!m.tables.length) errs.push('no .tablewrap beside the card');
  for (const tw of m.tables) {
    if (tw.overflowX !== 'auto' && tw.overflowX !== 'scroll') errs.push(`tablewrap overflow-x ${tw.overflowX}`);
    if (tw.right > m.clientWidth + 0.5) errs.push(`tablewrap right ${tw.right} past ${m.clientWidth}`);
  }
  const ok = errs.length === 0;
  if (!ok) failed++;
  results.push({ target: t.name, ok, errs, measure: m, screenshot: shot });
  console.log(`${ok ? 'PASS' : 'FAIL'} ${t.name}: scrollWidth=${m.scrollWidth} clientWidth=${m.clientWidth} fingerprintLines=${m.code?.lines} full=${m.code?.full} tables=${m.tables.map(x => x.overflowX + (x.scrollWidth > x.clientWidth ? '(scrolls)' : '(fits)')).join(',')}${errs.length ? '\n  ' + errs.join('\n  ') : ''}`);
  await context.close();
}
await browser.close();
fs.writeFileSync('acceptance-228.json', JSON.stringify({ base: BASE, path: PATH, at: new Date().toISOString(), results }, null, 2));
process.exit(failed ? 1 : 0);
