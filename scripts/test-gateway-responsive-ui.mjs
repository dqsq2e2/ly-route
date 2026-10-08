import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { mkdir, writeFile } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { startGatewayFixture } from './fixtures/gateway-ui-fixture.mjs';

// Supply a freshly built controller-shell bundle, or a live URL for read-only acceptance.
const require = createRequire(import.meta.url);
const { chromium } = require(process.env.GATEWAY_UI_PLAYWRIGHT_MODULE || 'playwright');
const bundleArg = process.argv.indexOf('--bundle');
const liveURL = process.env.GATEWAY_UI_URL;
assert.ok(liveURL || bundleArg >= 0, 'use --bundle DIRECTORY or GATEWAY_UI_URL');
const fixture = liveURL ? null : await startGatewayFixture({ bundleDir: resolve(process.argv[bundleArg + 1]) });
const baseURL = liveURL || fixture.url;
const widths = [320, 390, 600, 720, 800, 900, 901, 1440];
const evidenceDir = process.env.GATEWAY_UI_EVIDENCE_DIR;
if (evidenceDir) await mkdir(evidenceDir, { recursive: true });
const reports = [];
const browser = await chromium.launch({ channel: 'chrome', headless: true });

async function installMock(page) {
  const reply = (route, json) => route.fulfill({ contentType: 'application/json', body: JSON.stringify(json) });
  await page.route('**/api/v1/wifi', route => reply(route, {
    config: { enabled: true, mode: 'ap', ssid: 'Responsive QA', password_set: true, security: 'wpa2', country: 'CN', band: '5g', channel: 149, width: 80, isolate: true },
    status: { state: 'ap_ready', interface: 'wlp1s0', addresses: ['192.168.89.1/24'], business: { state: 'forwarding_ready' }, regulatory: { active: 'CN' }, capabilities: { channels: [{ band: '5g', channel: 149, frequency: 5745 }] }, stations: [] }
  }));
  await page.route('**/api/system/fan', route => reply(route, {
    available: true,
    config: {
      mode: 'curve', manual_pwm: 31, temp_source: 'cpu', cpu_statistic: 'max', cpu_sensor: 'temp2_input',
      curve_profile: 'linear', min_pwm: 16, stop_temperature: 42, start_temperature: 45,
      full_temperature: 60, poll_interval: 3,
      curve: [{ temperature: 45, pwm: 16 }, { temperature: 50, pwm: 43 }, { temperature: 55, pwm: 71 }, { temperature: 60, pwm: 100 }]
    },
    status: { running: true, output_pwm: 31, temperatures: { cpu: 48, wifi: 43, board: 42 } }
  }));
  await page.route('**/api/v1/auth/users', route => reply(route, { items: [{ username: 'admin', role: 'admin', enabled: true }] }));
  await page.route('**/api/v1/telemetry/top-domains', route => reply(route, {
    items: [{ domain: 'long-domain-name-for-mobile-layout.example.com', last_seen: '2026-10-09T00:00:00Z', hits: 100 }]
  }));
}

async function assertLayout(page, width, route) {
  const layout = await page.evaluate(() => {
    const rect = selector => {
      const e = document.querySelector(selector), r = e.getBoundingClientRect();
      return { left: r.left, right: r.right, width: r.width, top: r.top, bottom: r.bottom };
    };
    const main = document.querySelector('.paui-body');
    const card = document.querySelector('.page-card');
    const workspace = document.querySelector('#workspace');
    const cells = [...document.querySelectorAll('.list-content > .data-table tbody tr:not(.placeholder-row):not(.empty-row) td')];
    const tables = [...document.querySelectorAll('.list-content > .data-table')];
    return {
      main: rect('.paui-body'), header: rect('.paui-header'), card: rect('.page-card'),
      sidebar: rect('.paui-sidebar'),
      padding: parseFloat(getComputedStyle(workspace).paddingLeft) + parseFloat(getComputedStyle(workspace).paddingRight),
      mainOverflow: main.scrollWidth - main.clientWidth,
      cardOverflow: card.scrollWidth - card.clientWidth,
      mobileTables: tables.map(e => ({ display: getComputedStyle(e).display, overflow: e.scrollWidth - e.clientWidth })),
      unlabeledCells: cells.filter(e => !e.dataset.label).length,
      nonGridCells: cells.filter(e => getComputedStyle(e).display !== 'grid').map(e => e.dataset.label),
      clippedCells: cells.filter(e => e.scrollWidth > e.clientWidth + 1 || e.scrollHeight > e.clientHeight + 1)
        .map(e => ({ label: e.dataset.label, width: e.clientWidth, scrollWidth: e.scrollWidth, height: e.clientHeight, scrollHeight: e.scrollHeight,
          children: [...e.children].map(c => ({ width: c.getBoundingClientRect().width, height: c.getBoundingClientRect().height, whiteSpace: getComputedStyle(c).whiteSpace })) })),
      placeholdersVisible: [...document.querySelectorAll('.list-content > .data-table .placeholder-row')]
        .filter(e => e.getClientRects().length).length
    };
  });
  const where = `${width}px ${route}`;
  assert.ok(Math.abs(layout.main.top - layout.header.bottom) <= 1, `${where}: header/main alignment ${JSON.stringify(layout)}`);
  assert.ok(Math.abs(layout.card.width - (layout.main.width - layout.padding)) <= 1, `${where}: card must fill workspace`);
  assert.ok(layout.mainOverflow <= 1 && layout.cardOverflow <= 1, `${where}: content must contain horizontal scrolling`);
  if (width <= 900) {
    assert.equal(layout.main.left, 0, `${where}: mobile content starts at viewport edge`);
    assert.equal(layout.main.width, width, `${where}: shared shell must use the whole viewport`);
  } else {
    assert.ok(layout.main.left >= 200 && layout.main.right === width, `${where}: desktop sidebar retained`);
  }
  if (width <= 720) {
    assert.ok(layout.mobileTables.every(t => t.display === 'block' && t.overflow <= 1), `${where}: mobile cards cannot retain desktop table width`);
    assert.equal(layout.unlabeledCells, 0, `${where}: mobile fields must retain their column names`);
    assert.deepEqual(layout.nonGridCells, [], `${where}: mobile fields must align labels and values`);
    assert.deepEqual(layout.clippedCells, [], `${where}: mobile values must fit without clipping`);
    assert.equal(layout.placeholdersVisible, 0, `${where}: desktop filler rows must not become blank cards`);
  }
  return layout;
}

try {
  for (const width of widths) {
    const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true, viewport: { width, height: 844 } });
    try {
      if (liveURL) {
        assert.ok(process.env.GATEWAY_UI_PASSWORD, 'live acceptance needs GATEWAY_UI_PASSWORD');
        const login = await context.request.post('/api/v1/auth/login', {
          data: { username: process.env.GATEWAY_UI_USERNAME || 'admin', password: process.env.GATEWAY_UI_PASSWORD }
        });
        assert.equal(login.ok(), true, 'login must succeed');
      }
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      if (!liveURL) await installMock(page);
      await page.goto('/#monitor/interface_list');
      await page.locator('.nic-table td[data-label="接口"]').first().waitFor();
      const routes = await page.locator('#sideMenu [data-page]').evaluateAll(nodes => nodes.map(e => e.dataset.page));
      assert.ok(routes.length >= 18, 'cover the full gateway menu');
      if (!routes.includes('system/fan')) routes.push('system/fan');
      for (const route of routes) {
        await page.goto('/#' + route);
        await page.locator('#workspace .page-title h1').waitFor();
        if (route === 'network/wifi') await page.locator('[data-wifi-form]').waitFor();
        if (route === 'system/fan') await page.locator('[data-fan-field="mode"]').waitFor();
        const layout = await assertLayout(page, width, route);
        reports.push({ width, route, layout });
        if (width <= 900) {
          await page.locator('#mobileMenuToggle').click();
          await page.waitForFunction(() => document.querySelector('.paui-sidebar').getBoundingClientRect().left >= -1);
          assert.equal(await page.locator('#mobileMenuToggle').getAttribute('aria-expanded'), 'true');
          assert.ok(await page.locator('.paui-sidebar').evaluate(e => e.getBoundingClientRect().width > 200), 'menu must actually appear');
          await assertLayout(page, width, route);
          await page.keyboard.press('Escape');
          assert.equal(await page.locator('#mobileMenuToggle').getAttribute('aria-expanded'), 'false');
        }
        if (evidenceDir && [390, 1440].includes(width) &&
            ['monitor/interface_list', 'network/dhcpsvr_main', 'network/wifi', 'system/fan'].includes(route)) {
          await page.screenshot({ path: join(evidenceDir, `${route.replaceAll('/', '-')}-${width}.png`) });
        }
      }
      if (width <= 900) {
        await page.locator('#mobileMenuToggle').click();
        await page.locator('[data-page="monitor/interface_list"]').click();
        assert.equal(await page.locator('#mobileMenuToggle').getAttribute('aria-expanded'), 'false', 'navigation closes the overlay menu');
        await assertLayout(page, width, 'menu navigation');
      }
      if (width === 390) {
        await page.setViewportSize({ width: 1440, height: 844 });
        await assertLayout(page, 1440, 'resize to desktop');
        assert.equal(await page.locator('.paui-sidebar').isVisible(), true, 'desktop sidebar returns after a narrow-screen visit');
        await page.setViewportSize({ width: 390, height: 844 });
        await assertLayout(page, 390, 'resize to mobile');
        assert.equal(await page.locator('.paui-sidebar').isVisible(), false, 'mobile sidebar no longer consumes layout space');
      }
      assert.deepEqual(errors, [], `${width}px: no browser exceptions`);
      console.log(`Responsive UI passed: ${width}px, ${routes.length} pages, menu overlay and navigation.`);
    } finally {
      await context.close();
    }
  }
} finally {
  await browser.close();
  if (fixture) await fixture.close();
  if (evidenceDir) await writeFile(join(evidenceDir, 'responsive-layout.json'), JSON.stringify(reports, null, 2));
}
