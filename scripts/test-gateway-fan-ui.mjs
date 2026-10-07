import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { createRequire } from 'node:module';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { createServer } from 'node:http';
import { startGatewayFixture } from './fixtures/gateway-ui-fixture.mjs';

// Mock API browser verification only; this is not daemon or hardware acceptance.
const require = createRequire(import.meta.url);
const previewMode = process.argv.includes('--preview');
const { chromium } = previewMode ? {} : require(process.env.GATEWAY_FAN_PLAYWRIGHT_MODULE || 'playwright');
const run = promisify(execFile);
const repoRoot = join(import.meta.dirname, '..');
const temporary = previewMode ? null : await mkdtemp(join(tmpdir(), 'ly-route-fan-ui-'));
const evidenceDir = previewMode ? null : process.env.FAN_UI_EVIDENCE_DIR || await mkdtemp(join(tmpdir(), 'ly-route-fan-ui-evidence-'));
const clone = (value) => structuredClone(value);
const defaultConfig = {
  mode: 'curve', manual_pwm: 31, temp_source: 'cpu', cpu_statistic: 'max', cpu_sensor: 'temp2_input',
  curve_profile: 'linear', min_pwm: 16, stop_temperature: 42, start_temperature: 45,
  full_temperature: 60, poll_interval: 3,
  curve: [{ temperature: 45, pwm: 16 }, { temperature: 50, pwm: 43 }, { temperature: 55, pwm: 71 }, { temperature: 60, pwm: 100 }]
};
const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'compact', width: 1024, height: 768 },
  { name: 'mobile', width: 390, height: 844 }
];

async function installMock(page, overrides = {}) {
  const state = {
    available: true, role: 'admin', config: clone(defaultConfig), behavior: 'normal',
    gets: 0, puts: [], requests: [], failGET: false, releasePUT: null,
    status: {
      running: true, output_pwm: 31, mode: 'curve', config_revision: 1,
      effective_temperature: 58, updated_at: Math.floor(Date.now() / 1000), error: '',
      temperatures: { cpu: 40, wifi: null, board: 42 },
      sensors: [
        { source: 'cpu', sensor: 'temp2_input', label: 'Core 0', value: 47 },
        { source: 'cpu', sensor: 'temp3_input', label: 'Core 1', value: 58 },
        { source: 'board', sensor: 'temp1_input', label: 'Board 1', value: 46 },
        { source: 'board', sensor: 'temp2_input', label: 'Board 2', value: 49 }
      ]
    },
    ...overrides
  };
  const reply = (route, status, json) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(json) });
  await page.route('**/api/v1/auth/session', (route) => reply(route, 200, { session: { username: 'mock-fan-ui', role: state.role } }));
  await page.route('**/api/v1/auth/login', (route) => reply(route, 200, { session: { username: 'mock-fan-ui', role: state.role } }));
  await page.route('**/api/system/fan', async (route) => {
    const method = route.request().method();
    state.requests.push(method);
    if (method === 'PUT') {
      const payload = route.request().postDataJSON();
      state.puts.push(clone(payload));
      if (state.behavior === 'delay') await new Promise((resolve) => { state.releasePUT = resolve; });
      if (state.behavior === 'reject') return reply(route, 422, { error: { message: '模拟 API 拒绝配置' } });
      const previous = clone(state.config);
      if (state.behavior !== 'false-success') state.config = clone(payload);
      if (state.behavior === 'readback-failure') state.failGET = true;
      return reply(route, 200, {
        available: state.available, powered: false,
        config: state.behavior === 'stale-mutation' ? previous : payload,
        status: state.status
      });
    }
    assert.equal(method, 'GET', 'fan API only uses GET and PUT');
    state.gets++;
    if (state.failGET) return reply(route, 503, { error: { message: '模拟 API 暂时离线' } });
    return reply(route, 200, { available: state.available, powered: false, config: state.config, status: state.status });
  });
  return state;
}

const field = (page, key) => page.locator(`[data-fan-field="${key}"]`);
const saveButton = (page) => page.locator('[data-fan-save]');
const cancelButton = (page) => page.locator('[data-fan-cancel]');
const tooltip = (page) => page.getByRole('tooltip');
async function waitMessage(page, text) {
  await page.locator('[data-fan-message]').filter({ hasText: text }).waitFor();
  await page.waitForFunction(() => document.querySelector('[data-fan-form]')?.getAttribute('aria-busy') === 'false');
}
async function waitOutput(page, pwm) {
  await page.waitForFunction((value) => document.querySelector('[data-fan-service]')?.textContent === `控制服务运行中 · 当前输出 PWM ${value}%`, pwm, { timeout: 9000 });
}
async function assertLayout(page) {
  const failures = await page.evaluate(() => {
    const issues = [];
    if (document.documentElement.scrollWidth > innerWidth) issues.push('document overflows');
    if (innerWidth <= 900 && document.querySelector('.paui-body').getBoundingClientRect().width < innerWidth - 1) issues.push('mobile workspace fails to fill the viewport');
    for (const node of document.querySelectorAll('.fan-field, .fan-control, .fan-range, .fan-actions, .fan-actions button')) {
      if (!node.getClientRects().length) continue;
      const box = node.getBoundingClientRect();
      if (box.left < -1 || box.right > innerWidth + 1) issues.push(`${node.className} leaves viewport`);
      if (node.scrollWidth > node.clientWidth + 1) issues.push(`${node.className} content overflows`);
    }
    for (const range of document.querySelectorAll('.fan-range')) {
      if (!range.getClientRects().length) continue;
      const input = range.querySelector('input').getBoundingClientRect();
      const output = range.querySelector('output').getBoundingClientRect();
      if (input.right > output.left) issues.push('range overlaps percentage');
    }
    return issues;
  });
  assert.deepEqual(failures, [], 'visible form controls must fit without overlap');
  assert.equal(await page.locator('.fan-actions').evaluate((node) => getComputedStyle(node).position), 'static', 'settings footer must not float');
}

async function assertHelp(page) {
  const modeHelp = page.getByRole('button', { name: '控制方式帮助', exact: true });
  const temperatureHelp = page.getByRole('button', { name: '温度源帮助', exact: true });
  assert.equal(await page.locator('[data-fan-help]').count(), await page.locator('[data-fan-field]').count(), 'each editable control has exactly one help button');
  assert.equal(await page.locator('[role="tooltip"]').count(), 1, 'page owns exactly one shared tooltip');
  await modeHelp.hover();
  await tooltip(page).waitFor();
  assert.equal(await modeHelp.getAttribute('aria-expanded'), 'true');
  await modeHelp.click();
  await page.getByRole('heading', { name: '温度控制', exact: true }).hover();
  assert.equal(await tooltip(page).isVisible(), true, 'click pins the hovered help');
  await temperatureHelp.click();
  assert.equal(await tooltip(page).count(), 1, 'switching help must not duplicate the bubble');
  assert.equal(await modeHelp.getAttribute('aria-expanded'), 'false');
  assert.equal(await temperatureHelp.getAttribute('aria-expanded'), 'true');
  assert.equal(await tooltip(page).textContent().then((text) => text.includes('组合选项')), true);
  await page.keyboard.press('Escape');
  assert.equal(await tooltip(page).count(), 0, 'Escape closes help');
  assert.equal(await temperatureHelp.getAttribute('aria-expanded'), 'false');
  await modeHelp.focus();
  await page.keyboard.press('Enter');
  assert.equal(await tooltip(page).isVisible(), true, 'keyboard can pin help');
  await page.getByRole('heading', { name: '温度控制', exact: true }).click();
  assert.equal(await tooltip(page).count(), 0, 'outside click closes help');
  await modeHelp.click();
  await modeHelp.click();
  assert.equal(await tooltip(page).count(), 0, 'second click toggles pinned help off');
}

async function assertRangeAndPolling(page, mock) {
  await field(page, 'mode').selectOption('manual');
  assert.equal(await saveButton(page).isEnabled(), true, 'mode switch immediately makes the draft saveable');
  assert.equal(await field(page, 'temp_source').isVisible(), false);
  const slider = field(page, 'manual_pwm');
  assert.equal(await slider.inputValue(), '31', 'manual default is 31 percent');
  assert.equal(await slider.evaluate((node) => getComputedStyle(node).padding), '0px', 'text input padding must not displace range endpoints');
  assert.equal(await slider.evaluate((node) => getComputedStyle(node).borderWidth), '0px');
  const box = await slider.boundingBox();
  await page.mouse.click(box.x + 1, box.y + box.height / 2);
  assert.equal(await slider.inputValue(), '0', 'native range reaches the true left endpoint');
  assert.equal(await page.locator('[data-fan-output="manual_pwm"]').textContent(), '0%');
  await page.mouse.click(box.x + box.width - 1, box.y + box.height / 2);
  assert.equal(await slider.inputValue(), '100', 'native range reaches the true right endpoint');
  assert.equal(await page.locator('[data-fan-output="manual_pwm"]').textContent(), '100%');
  await slider.focus();
  await page.keyboard.press('Home');
  assert.equal(await slider.inputValue(), '0', 'range retains native keyboard endpoints');
  await page.keyboard.press('End');
  assert.equal(await slider.inputValue(), '100');

  await slider.evaluate((node) => { node.dataset.nodeIdentity = 'original-range'; });
  await page.mouse.move(box.x + box.width - 8, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width * 0.7, box.y + box.height / 2);
  const draggedValue = await slider.inputValue();
  const before = mock.gets;
  mock.status.output_pwm = 82;
  await waitOutput(page, 82);
  assert.equal(mock.gets > before, true, 'status polls while the range is held');
  assert.equal(await slider.getAttribute('data-node-identity'), 'original-range', 'poll does not replace the native range node');
  assert.equal(await slider.inputValue(), draggedValue, 'dirty slider value survives status poll');
  assert.equal(await field(page, 'mode').inputValue(), 'manual', 'status.mode never overwrites draft mode');
  await page.mouse.up();
  await page.getByRole('heading', { name: '温度控制', exact: true }).click();
  const generalRefresh = page.waitForResponse((response) => response.url().includes('/api/v1/health'));
  await generalRefresh;
  assert.equal(await slider.getAttribute('data-node-identity'), 'original-range', 'global telemetry refresh also preserves the form DOM');
  assert.equal(await slider.inputValue(), draggedValue);

  await slider.focus();
  await page.keyboard.press('Home');
  mock.behavior = 'delay';
  const submitted = page.waitForRequest((request) => request.url().endsWith('/api/system/fan') && request.method() === 'PUT');
  await saveButton(page).click();
  const request = await submitted;
  assert.equal(request.postDataJSON().manual_pwm, 0, 'zero is serialized without default substitution');
  await page.getByRole('button', { name: '保存中…', exact: true }).waitFor();
  assert.equal(await slider.isDisabled(), true, 'controls are disabled during save');
  assert.equal(await cancelButton(page).isDisabled(), true);
  mock.releasePUT();
  await waitMessage(page, '配置已保存');
  mock.behavior = 'normal';
  assert.equal(mock.requests.slice(-2).join(','), 'PUT,GET', 'save is confirmed through GET readback');
  assert.equal(await field(page, 'mode').inputValue(), 'manual', 'saved config wins over pending runtime mode');
  assert.equal(await slider.inputValue(), '0');
  assert.equal(mock.status.mode, 'curve', 'mock intentionally leaves daemon mode pending');
  await waitOutput(page, 82);
  assert.equal(await saveButton(page).isDisabled(), true, 'confirmed save is clean');
  await slider.focus();
  await page.keyboard.press('End');
  await cancelButton(page).click();
  assert.equal(await slider.inputValue(), '0', 'cancel restores GET-confirmed config, not output PWM');
}

async function assertAdvancedAndValidation(page, mock) {
  await field(page, 'mode').selectOption('curve');
  assert.equal(await saveButton(page).isEnabled(), true);
  await page.locator('[data-fan-advanced] > summary').click();
  await field(page, 'cpu_statistic').selectOption('single');
  await field(page, 'cpu_sensor').selectOption('temp3_input');
  await field(page, 'curve_profile').selectOption('custom');
  await page.getByRole('button', { name: '风速曲线帮助', exact: true }).click();
  assert.equal(await tooltip(page).textContent().then((text) => text.includes('超过最后节点') && text.includes('100% PWM')), true, 'curve help declares full-speed safety above the final point');
  await page.keyboard.press('Escape');
  assert.equal(await field(page, 'min_pwm').isVisible(), false, 'low-temperature PWM is advanced and linear-only');
  assert.equal(await page.locator('.fan-curve [data-fan-field]').count(), 8, 'custom curve exposes exactly four temperature/PWM pairs');
  const before = mock.puts.length;
  await field(page, 'full_temperature').fill('44');
  await saveButton(page).click();
  await waitMessage(page, '满速温度必须高于起转温度');
  assert.equal(mock.puts.length, before, 'invalid temperature thresholds never reach PUT');
  await field(page, 'full_temperature').fill('60');
  await field(page, 'stop_temperature').fill('45');
  await saveButton(page).click();
  await waitMessage(page, '停转温度必须低于起转温度');
  assert.equal(mock.puts.length, before);
  await field(page, 'stop_temperature').fill('0');
  await field(page, 'curve-1-temperature').fill('44');
  await saveButton(page).click();
  await waitMessage(page, '曲线温度必须逐点递增');
  assert.equal(mock.puts.length, before);
  await field(page, 'curve-1-temperature').fill('50');
  await field(page, 'poll_interval').fill('31');
  await saveButton(page).click();
  await waitMessage(page, '采样间隔必须为 1 到 30 的整数');
  assert.equal(mock.puts.length, before);
  await field(page, 'poll_interval').fill('3');
  await field(page, 'curve-3-pwm').focus();
  await page.keyboard.press('End');
  await page.keyboard.press('ArrowLeft');
  await saveButton(page).click();
  await waitMessage(page, '配置已保存');
  const saved = mock.puts.at(-1);
  assert.equal(saved.cpu_statistic, 'single');
  assert.equal(saved.cpu_sensor, 'temp3_input');
  assert.equal(saved.curve_profile, 'custom');
  assert.equal(saved.stop_temperature, 0, 'always-run sentinel is preserved');
  const expectedCurve = clone(defaultConfig.curve);
  expectedCurve[3].pwm = 99;
  assert.deepEqual(saved.curve, expectedCurve, 'final node PWM may be below 100; safety behavior applies above the node');
  assert.equal(await field(page, 'cpu_sensor').inputValue(), 'temp3_input', 'CPU selection survives readback');
}

async function assertFailureStates(page, mock) {
  await field(page, 'mode').selectOption('manual');
  await field(page, 'manual_pwm').focus();
  await page.keyboard.press('End');
  mock.behavior = 'false-success';
  await saveButton(page).click();
  await waitMessage(page, '保存响应与 API 回读不一致');
  assert.equal(await field(page, 'mode').inputValue(), 'manual');
  assert.equal(await field(page, 'manual_pwm').inputValue(), '100', 'false success preserves the operator draft');
  assert.equal(await saveButton(page).isEnabled(), true);
  assert.equal(await page.locator('[data-fan-message]').textContent().then((text) => text.includes('配置已保存')), false);
  mock.behavior = 'reject';
  await saveButton(page).click();
  await waitMessage(page, '模拟 API 拒绝配置');
  assert.equal(await field(page, 'manual_pwm').inputValue(), '100');
  mock.behavior = 'readback-failure';
  await saveButton(page).click();
  await waitMessage(page, '模拟 API 暂时离线');
  assert.equal(await saveButton(page).isEnabled(), true, 'failed GET cannot claim a confirmed save');
  mock.behavior = 'normal';
  mock.failGET = false;
  await saveButton(page).click();
  await waitMessage(page, '配置已保存');
  await field(page, 'manual_pwm').focus();
  await page.keyboard.press('Home');
  mock.behavior = 'stale-mutation';
  await saveButton(page).click();
  await waitMessage(page, '保存响应与 API 回读不一致');
  assert.equal(await field(page, 'manual_pwm').inputValue(), '0', 'unconfirmed PUT response cannot erase the draft');
  mock.behavior = 'normal';
  await field(page, 'manual_pwm').focus();
  await page.keyboard.press('End');
  await saveButton(page).click();
  await waitMessage(page, '配置已保存');
  mock.status.error = '模拟传感器读取故障';
  mock.status.output_pwm = null;
  await page.locator('[data-fan-status-error]').filter({ hasText: '模拟传感器读取故障' }).waitFor();
  assert.equal(await page.locator('[data-fan-service]').textContent(), '控制服务运行中 · 当前输出 PWM --', 'unknown output must not become zero');
  mock.status.error = '';
  mock.status.output_pwm = 0;
  await waitOutput(page, 0);
}

async function assertWiFiGroups(page, mock) {
  mock.status.sensors.push(
    { source: 'wifi', sensor: 'wifi0', label: 'WiFi 0', value: 39 },
    { source: 'wifi', sensor: 'wifi1', label: 'WiFi 1', value: 67 }
  );
  mock.status.temperatures.wifi = 50;
  await page.waitForFunction(() => document.querySelector('[data-fan-temperature="wifi"]')?.textContent === '67.0 °C');
  mock.status.sensors = mock.status.sensors.filter((sensor) => sensor.source !== 'wifi');
  mock.status.temperatures.wifi = null;
  await page.waitForFunction(() => document.querySelector('[data-fan-temperature="wifi"]')?.textContent === '--');
}

async function assertSessionRestart(page, mock) {
  await field(page, 'manual_pwm').focus();
  await page.keyboard.press('Home');
  assert.equal(await saveButton(page).isEnabled(), true);
  for (const role of ['readonly', 'admin']) {
    mock.role = role;
    await page.getByRole('button', { name: '退出', exact: true }).click();
    await page.locator('#loginScreen').waitFor();
    await page.locator('#username').fill('mock-fan-ui');
    await page.locator('#password').fill('mock-password');
    await page.getByRole('button', { name: '登录', exact: true }).click();
    await field(page, 'mode').waitFor();
    assert.equal(await field(page, 'manual_pwm').inputValue(), '100', 'new session uses confirmed config, not another account draft');
    assert.equal(await field(page, 'mode').isDisabled(), role === 'readonly', 'new session refreshes fan permissions');
    assert.equal(await saveButton(page).isDisabled(), true);
  }
}

async function runViewport(browser, baseURL, variant, viewport) {
  const context = await browser.newContext({ baseURL, viewport: { width: viewport.width, height: viewport.height } });
  const page = await context.newPage();
  page.setDefaultTimeout(12000);
  const pageErrors = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  const mock = await installMock(page);
  const name = `mock-${variant}-${viewport.name}`;
  try {
    await page.goto('/#system/fan');
    await field(page, 'mode').waitFor();
    await waitOutput(page, 31);
    assert.equal(await page.locator('[data-page="system/fan"]').count(), 1, 'supported hardware has a System Maintenance entry');
    assert.equal(await field(page, 'mode').inputValue(), 'curve');
    assert.equal(await field(page, 'temp_source').inputValue(), 'cpu');
    assert.deepEqual(await field(page, 'temp_source').locator('option').allTextContents(), [
      'CPU 温度', 'WiFi 温度', '主板温度', 'CPU / WiFi / 主板最高温', 'CPU / WiFi / 主板平均温'
    ]);
    assert.equal(await page.locator('[data-fan-temperature="cpu"]').textContent(), '58.0 °C', 'CPU status is highest within its sensor group');
    assert.equal(await page.locator('[data-fan-temperature="board"]').textContent(), '49.0 °C', 'board status is highest within its sensor group');
    assert.equal(await page.locator('[data-fan-temperature="wifi"]').textContent(), '--', 'missing WiFi is never invented');
    assert.equal(await field(page, 'min_pwm').isVisible(), false, 'low-temperature speed is not a basic setting');
    await assertLayout(page);
    await page.screenshot({ path: join(evidenceDir, `${name}-automatic.png`), fullPage: true });
    await assertHelp(page);
    if (viewport.name === 'desktop') await assertWiFiGroups(page, mock);
    await assertRangeAndPolling(page, mock);
    await assertLayout(page);
    await page.screenshot({ path: join(evidenceDir, `${name}-manual.png`), fullPage: true });
    await assertAdvancedAndValidation(page, mock);
    await assertLayout(page);
    await page.screenshot({ path: join(evidenceDir, `${name}-advanced.png`), fullPage: true });
    await page.getByRole('button', { name: '风速曲线帮助', exact: true }).click();
    await page.screenshot({ path: join(evidenceDir, `${name}-curve-help.png`), fullPage: true });
    await page.keyboard.press('Escape');
    if (viewport.name === 'desktop') {
      await assertFailureStates(page, mock);
      await assertSessionRestart(page, mock);
    }
    assert.deepEqual(pageErrors, [], 'no uncaught browser errors');
    await writeFile(join(evidenceDir, `${name}-requests.json`), JSON.stringify({
      scope: 'Mock UI verification only; not hardware or daemon acceptance',
      requests: mock.requests, puts: mock.puts, pageErrors
    }, null, 2));
    console.log(`PASS ${name}`);
  } finally {
    mock.releasePUT?.();
    await context.close();
  }
}

async function assertUnavailableAndReadonly(browser, baseURL, variant) {
  for (const condition of ['unavailable', 'readonly', 'initial-error', 'missing-cpu-sensor']) {
    const context = await browser.newContext({ baseURL, viewport: { width: 390, height: 844 } });
    const page = await context.newPage();
    const mock = await installMock(page, condition === 'unavailable' ? { available: false } : condition === 'readonly' ? { role: 'readonly' }
      : condition === 'initial-error' ? { failGET: true } : { config: { ...clone(defaultConfig), cpu_sensor: 'temp0_input' } });
    try {
      await page.goto('/#system/fan');
      if (condition === 'unavailable') {
        await page.getByText('当前硬件不支持温度控制', { exact: true }).waitFor();
        assert.equal(await page.locator('[data-page="system/fan"]').count(), 0, 'generic hardware hides the menu entry after GET');
        assert.equal(await page.locator('[data-fan-field]').count(), 0, 'direct navigation has no editable unsupported controls');
      } else if (condition === 'readonly') {
        await field(page, 'mode').waitFor();
        assert.equal(await field(page, 'mode').isDisabled(), true);
        await page.locator('[data-fan-advanced] > summary').click();
        for (const control of await page.locator('[data-fan-field]').all()) assert.equal(await control.isDisabled(), true, 'all readonly fields are disabled');
        assert.equal(await saveButton(page).isDisabled(), true);
        await page.locator('[data-fan-form]').evaluate((form) => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
        assert.equal(mock.puts.length, 0, 'readonly submit guard prevents mutation');
        await page.getByRole('button', { name: '控制方式帮助', exact: true }).click();
        assert.equal(await tooltip(page).isVisible(), true, 'readonly users can still read help');
        await page.keyboard.press('Escape');
      } else if (condition === 'initial-error') {
        await page.getByText('模拟 API 暂时离线', { exact: true }).waitFor();
        mock.failGET = false;
        await page.getByRole('button', { name: '重试', exact: true }).click();
        await field(page, 'mode').waitFor();
        assert.equal(await field(page, 'mode').inputValue(), 'curve', 'initial GET error can recover');
      } else {
        await field(page, 'mode').waitFor();
        await page.locator('[data-fan-advanced] > summary').click();
        await field(page, 'cpu_statistic').selectOption('single');
        assert.equal(await field(page, 'cpu_sensor').inputValue(), 'temp0_input', 'frontend accepts backend temp[0-9]+ sensor contract');
        assert.equal(await field(page, 'cpu_sensor').locator('option:checked').textContent().then((text) => text.includes('当前不可用')), true, 'missing configured sensor is retained and identified');
        await page.getByRole('button', { name: 'CPU 核心帮助', exact: true }).click();
        assert.equal(await tooltip(page).textContent().then((text) => text.includes('100% PWM') && text.includes('不替换')), true, 'help declares missing sensor safety behavior');
        await page.keyboard.press('Escape');
        await saveButton(page).click();
        await waitMessage(page, '配置已保存');
        assert.equal(mock.puts.at(-1).cpu_sensor, 'temp0_input');
      }
      assert.equal(mock.puts.length, condition === 'missing-cpu-sensor' ? 1 : 0);
      await page.screenshot({ path: join(evidenceDir, `mock-${variant}-${condition}.png`), fullPage: true });
      console.log(`PASS mock-${variant}-${condition}`);
    } finally {
      await context.close();
    }
  }
}

async function startPreview() {
  const portIndex = process.argv.indexOf('--port');
  const port = portIndex >= 0 ? Number(process.argv[portIndex + 1]) : 0;
  assert.equal(Number.isInteger(port) && port >= 0 && port <= 65535, true, 'preview port must be 0..65535');
  const fixture = await startGatewayFixture({ bundleDir: join(repoRoot, 'frontend/gateway') });
  const config = clone(defaultConfig);
  const status = {
    running: true, output_pwm: 31, mode: 'curve', config_revision: 'mock-1',
    temperatures: { cpu: 58, wifi: null, board: 49 }, sensors: [
      { source: 'cpu', sensor: 'temp2_input', label: 'Core 0', value: 58 },
      { source: 'board', sensor: 'temp1_input', label: 'Board', value: 49 }
    ], effective_temperature: 58, error: '', updated_at: Math.floor(Date.now() / 1000)
  };
  const server = createServer(async (request, response) => {
    try {
      if (new URL(request.url, fixture.url).pathname === '/api/system/fan') {
        if (request.method === 'PUT') {
          const chunks = [];
          for await (const chunk of request) chunks.push(chunk);
          Object.assign(config, JSON.parse(Buffer.concat(chunks).toString('utf8')));
          status.mode = config.mode;
          status.output_pwm = config.mode === 'manual' ? config.manual_pwm : 31;
          status.config_revision = `mock-${Date.now()}`;
        }
        status.updated_at = Math.floor(Date.now() / 1000);
        response.writeHead(200, { 'content-type': 'application/json; charset=utf-8', 'x-ly-route-fan-mock': 'true' });
        response.end(JSON.stringify({ available: true, powered: true, config, status }));
        return;
      }
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const upstream = await fetch(new URL(request.url, fixture.url), {
        method: request.method,
        headers: request.headers['content-type'] ? { 'content-type': request.headers['content-type'] } : {},
        body: ['GET', 'HEAD'].includes(request.method) ? undefined : Buffer.concat(chunks)
      });
      response.writeHead(upstream.status, { 'content-type': upstream.headers.get('content-type') || 'application/octet-stream' });
      response.end(Buffer.from(await upstream.arrayBuffer()));
    } catch (error) {
      response.writeHead(500, { 'content-type': 'application/json' });
      response.end(JSON.stringify({ error: { message: error.message } }));
    }
  });
  const listen = (requestedPort) => new Promise((resolve, reject) => {
    const onError = (error) => { server.removeListener('listening', onListen); reject(error); };
    const onListen = () => { server.removeListener('error', onError); resolve(); };
    server.once('error', onError);
    server.once('listening', onListen);
    server.listen(requestedPort, '127.0.0.1');
  });
  try {
    try { await listen(port); } catch (error) {
      if (error.code !== 'EADDRINUSE') throw error;
      await listen(0);
    }
    console.log('MOCK UI preview only; not hardware or daemon acceptance.');
    console.log(`Preview: http://127.0.0.1:${server.address().port}/#system/fan`);
    console.log('Stop with Ctrl+C. No backend or hardware is modified.');
    await new Promise((resolve) => {
      process.once('SIGINT', resolve);
      process.once('SIGTERM', resolve);
    });
  } finally {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
    await fixture.close();
  }
}

async function runBrowserChecks() {
  let browser;
  const fixtures = [];
  let passed = false;
  try {
    await mkdir(evidenceDir, { recursive: true });
    const bundleDir = join(temporary, 'bundle');
    const bash = process.env.GATEWAY_FAN_BASH || (process.platform === 'win32' ? join(process.env.ProgramFiles || 'C:/Program Files', 'Git/bin/bash.exe') : 'bash');
    await run(bash, ['scripts/build-controller-shell.sh', '--product', 'gateway', '--out', bundleDir.replaceAll('\\', '/')], { cwd: repoRoot, windowsHide: true });
    const bundle = await readFile(join(bundleDir, 'app.js'), 'utf8');
    const html = await readFile(join(bundleDir, 'index.html'), 'utf8');
    assert.equal(bundle.includes('initializeGatewayFan'), true, 'release bundle includes the same fan module');
    assert.equal(html.includes('gateway-fan.js'), false, 'release does not request development modules');
    await readFile(join(bundleDir, 'commercial.css'), 'utf8');
    browser = await chromium.launch({ channel: process.env.FAN_UI_BROWSER_CHANNEL || 'chrome', headless: true });
    for (const [variant, directory] of [['development', join(repoRoot, 'frontend/gateway')], ['release', bundleDir]]) {
      const fixture = await startGatewayFixture({ bundleDir: directory });
      fixtures.push(fixture);
      for (const viewport of viewports) await runViewport(browser, fixture.url, variant, viewport);
      await assertUnavailableAndReadonly(browser, fixture.url, variant);
      await fixture.close();
      fixtures.pop();
    }
    passed = true;
  } finally {
    await browser?.close();
    for (const fixture of fixtures) await fixture.close();
    const resolved = join(temporary, '..');
    assert.equal(resolved, tmpdir(), 'cleanup target must stay within the temporary workspace');
    await rm(temporary, { recursive: true, force: true });
    await writeFile(join(evidenceDir, 'cleanup-receipt.json'), JSON.stringify({
      result: passed ? 'passed' : 'failed', scope: 'Mock UI only; not hardware acceptance',
      browser: 'closed', servers: 'closed', temporaryBundle: 'removed', evidenceDir
    }, null, 2));
  }
  console.log(`Gateway fan UI mock checks passed: development + release, 3 viewports each. Evidence: ${evidenceDir}`);
}

if (previewMode) await startPreview();
else await runBrowserChecks();
