// Takes docs/screenshot.png for the README.
//
//   make screenshot
//
// needs Node.js and Playwright (npm i -g playwright && npx playwright install
// chromium; or point PLAYWRIGHT at a playwright or playwright-core module and
// CHROME at a Chromium binary). The server must already be running at the URL
// given as the first argument; the PNG path is the second argument.
'use strict';

const { chromium } = require(process.env.PLAYWRIGHT || 'playwright');

const [base, png] = process.argv.slice(2);

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROME || undefined });
  const page = await browser.newPage({ viewport: { width: 1400, height: 780 }, deviceScaleFactor: 1 });
  await page.goto(base);
  await page.waitForTimeout(400);

  // A typical session: name a column type, pick the dialect.
  await page.click('.chip[data-c="1"]');
  await page.selectOption('#pc-type', 'varchar');
  await page.fill('#pc-len', '100');
  await page.keyboard.press('Escape');
  await page.selectOption('#opt-dialect', 'postgres');
  await page.click('td.cell[data-r="2"][data-c="0"]');
  await page.keyboard.type('3');
  await page.keyboard.press('Tab');
  await page.keyboard.type('Carol White');
  await page.keyboard.press('Tab');
  await page.keyboard.type('carol@example.com');
  await page.keyboard.press('Tab');
  await page.keyboard.type('2024-05-03 09:15:00');
  await page.mouse.click(5, 5); // finishes the edit
  await page.waitForTimeout(500);

  await page.screenshot({ path: png });
  await browser.close();
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
