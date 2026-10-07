(function initializeGatewayFan() {
  const endpoint = '/api/system/fan';
  const defaults = {
    mode: 'curve', manual_pwm: 31, temp_source: 'cpu', cpu_statistic: 'max', cpu_sensor: 'temp2_input',
    curve_profile: 'linear', min_pwm: 16, stop_temperature: 42, start_temperature: 45,
    full_temperature: 60, poll_interval: 3,
    curve: [{ temperature: 45, pwm: 16 }, { temperature: 50, pwm: 43 }, { temperature: 55, pwm: 71 }, { temperature: 60, pwm: 100 }]
  };
  const enumFields = {
    mode: ['curve', 'manual'], temp_source: ['cpu', 'wifi', 'board', 'max', 'average'],
    cpu_statistic: ['max', 'average', 'single'], curve_profile: ['linear', 'custom']
  };
  const numericFields = {
    manual_pwm: ['PWM 风速', 0, 100, true], min_pwm: ['低温风速', 0, 100, true],
    stop_temperature: ['停转温度', 0, 100], start_temperature: ['起转温度', 0, 100],
    full_temperature: ['满速温度', 0, 100], poll_interval: ['采样间隔', 1, 30, true]
  };
  const clone = (value) => JSON.parse(JSON.stringify(value));
  const configOnly = (value) => Object.fromEntries(Object.keys(defaults).map((key) => [key, key === 'curve'
    ? value?.curve?.map((point) => ({ temperature: point.temperature, pwm: point.pwm })) : value?.[key]]));
  const equal = (a, b) => JSON.stringify(configOnly(a)) === JSON.stringify(configOnly(b));

  function validate(config) {
    for (const [key, values] of Object.entries(enumFields)) {
      if (!values.includes(config[key])) return { key, message: '请选择有效的控制参数' };
    }
    for (const [key, [label, min, max, integer]] of Object.entries(numericFields)) {
      const value = config[key];
      if (!Number.isFinite(value) || value < min || value > max || (integer && !Number.isInteger(value))) {
        return { key, message: `${label}必须为 ${min} 到 ${max} 的${integer ? '整数' : '数值'}` };
      }
    }
    if (typeof config.cpu_sensor !== 'string' || !/^temp[0-9]+_input$/.test(config.cpu_sensor)) {
      return { key: 'cpu_sensor', message: '请选择有效的 CPU 核心传感器' };
    }
    if (config.full_temperature <= config.start_temperature) return { key: 'full_temperature', message: '满速温度必须高于起转温度' };
    if (config.stop_temperature !== 0 && config.stop_temperature >= config.start_temperature) return { key: 'stop_temperature', message: '停转温度必须低于起转温度；0 表示不停转' };
    if (!Array.isArray(config.curve) || config.curve.length !== 4) return { key: 'curve_profile', message: '自定义曲线必须包含四个节点' };
    for (let index = 0; index < 4; index++) {
      const point = config.curve[index];
      if (!point || !Number.isFinite(point.temperature) || point.temperature < 0 || point.temperature > 100) {
        return { key: `curve-${index}-temperature`, message: `曲线点 ${index + 1} 温度必须为 0 到 100 的数值` };
      }
      if (index && point.temperature <= config.curve[index - 1].temperature) {
        return { key: `curve-${index}-temperature`, message: '曲线温度必须逐点递增' };
      }
      if (!Number.isInteger(point.pwm) || point.pwm < 0 || point.pwm > 100) {
        return { key: `curve-${index}-pwm`, message: `曲线点 ${index + 1} PWM 必须为 0 到 100 的整数` };
      }
    }
    return null;
  }

  function checkedResponse(payload) {
    if (typeof payload?.available !== 'boolean') throw new Error('温控接口未返回有效的硬件状态');
    if (payload.available && (!payload.config || validate(payload.config))) throw new Error('温控接口未返回完整有效的配置');
    return payload;
  }

  function create({ apiJSON, safeText, isReadonly, onAvailability, toast }) {
    let available = null;
    let confirmed = null;
    let draft = clone(defaults);
    let status = null;
    let root = null;
    let timer = null;
    let readRequest = null;
    let generation = 0;
    let saving = false;
    let readError = '';
    let message = '';
    let messageKind = '';
    let events = null;
    let helpButton = null;
    let helpPinned = false;

    const dirty = () => confirmed && !equal(draft, confirmed);
    const fieldID = (key) => `fan-${key}`;
    function control(key, label, html, help, when = '') {
      return `<div class="fan-field" ${when ? `data-fan-when="${when}"` : ''}>
        <label for="${fieldID(key)}">${safeText(label)}</label>
        <div class="fan-control">${html}</div>
        <button class="fan-help" type="button" data-fan-help="${safeText(help)}" aria-label="${safeText(label)}帮助" aria-expanded="false" aria-controls="fan-help-tooltip">?</button>
      </div>`;
    }
    function select(key, label, options, help, when = '') {
      const html = `<select id="${fieldID(key)}" data-fan-field="${key}">${options.map(([value, text]) =>
        `<option value="${safeText(value)}" ${draft[key] === value ? 'selected' : ''}>${safeText(text)}</option>`).join('')}</select>`;
      return control(key, label, html, help, when);
    }
    function number(key, label, help, when = '') {
      const [, min, max, integer] = numericFields[key];
      return control(key, label, `<input id="${fieldID(key)}" data-fan-field="${key}" type="number" min="${min}" max="${max}" step="${integer ? '1' : 'any'}" value="${draft[key]}">`, help, when);
    }
    function range(key, label, help, when = '') {
      const parts = /^curve-(\d)-pwm$/.exec(key);
      const value = parts ? draft.curve[Number(parts[1])].pwm : draft[key];
      return control(key, label, `<div class="fan-range"><input id="${fieldID(key)}" data-fan-field="${key}" type="range" min="0" max="100" step="1" value="${value}"><output for="${fieldID(key)}" data-fan-output="${key}">${value}%</output></div>`, help, when);
    }
    function sensorOptions() {
      const sensors = new Map((status?.sensors || []).filter((sensor) => sensor.source === 'cpu' && sensor.sensor)
        .map((sensor) => [sensor.sensor, sensor.label || sensor.sensor]));
      if (!sensors.has(draft.cpu_sensor)) sensors.set(draft.cpu_sensor, `${draft.cpu_sensor}（当前不可用）`);
      return [...sensors].map(([sensor, label]) => [sensor, /^Core\s+(\d+)$/i.test(label) ? `CPU 核心 ${label.match(/\d+/)[0]}` : `CPU ${label}`]);
    }
    function formHTML() {
      return `<form class="fan-form" data-fan-form novalidate>
        ${select('mode', '控制方式', [['curve', '自动温控'], ['manual', '手动调速']], '自动温控按温度曲线调速；手动调速固定使用指定的 PWM。')}
        ${range('manual_pwm', 'PWM 风速 (%)', '手动模式固定使用的 PWM 输出百分比，不代表实际转速。0% 切断风扇供电以停转；大于 0% 使用 PWM 直通调速。', 'manual')}
        ${select('temp_source', '温度源', [['cpu', 'CPU 温度'], ['wifi', 'WiFi 温度'], ['board', '主板温度'], ['max', 'CPU / WiFi / 主板最高温'], ['average', 'CPU / WiFi / 主板平均温']], '选择自动调速使用的温度；组合选项按全部有效传感器取最高值或平均值，不补缺失值。指定温度源不可用时，控制服务使用 100% PWM 安全冷却并报告错误，不自动换源。', 'curve')}
        ${number('start_temperature', '起转温度 (°C)', '自动模式下温度达到此值后开始输出起转 PWM。', 'curve')}
        ${number('full_temperature', '满速温度 (°C)', '线性曲线在此温度达到 100% PWM，必须高于起转温度。', 'curve')}
        ${number('stop_temperature', '停转温度 (°C)', '自动模式下温度不高于此值时输出 0%；必须低于起转温度。填 0 表示始终输出，不启用停转。', 'curve')}
        <details class="fan-advanced" data-fan-advanced><summary>高级设置</summary><div class="fan-advanced-fields">
          ${range('min_pwm', '低温风速 (%)', '线性曲线低温端使用的 PWM；不影响手动模式。PWM 直通下 0% 仍可能保持最低转速，需要停转时使用停转温度。', 'linear')}
          ${select('cpu_statistic', 'CPU 温度统计', [['max', '核心最高温'], ['average', '核心平均温'], ['single', '单个核心']], '仅温度源为 CPU 时生效，可选择核心最高温、平均温或指定单个核心。', 'cpu')}
          ${select('cpu_sensor', 'CPU 核心', sensorOptions(), '单个核心模式使用的传感器。缺失时保留原配置并标记不可用，不替换为其他核心；自动控制无法读取此核心时，使用 100% PWM 安全冷却并报告错误。', 'single')}
          ${select('curve_profile', '风速曲线', [['linear', '线性升速'], ['custom', '自定义四点曲线']], '线性升速使用起转、满速温度和低温 PWM；自定义曲线按四个温度与 PWM 节点插值。温度超过最后节点时，强制使用 100% PWM 安全冷却。', 'curve')}
          <div class="fan-curve" data-fan-when="custom">${draft.curve.map((point, index) => {
            const key = `curve-${index}-temperature`;
            return control(key, `曲线点 ${index + 1} 温度 (°C)`, `<input id="${fieldID(key)}" data-fan-field="${key}" type="number" min="0" max="100" step="any" value="${point.temperature}">`, `自定义曲线第 ${index + 1} 个温度节点，四个节点温度必须逐点递增。`)
              + range(`curve-${index}-pwm`, `曲线点 ${index + 1} PWM (%)`, `自定义曲线在第 ${index + 1} 个温度节点使用的 PWM 输出百分比。`);
          }).join('')}</div>
          ${number('poll_interval', '采样间隔 (秒)', '控制服务读取温度并更新 PWM 的间隔，范围为 1 到 30 秒。页面状态每 3 秒读取一次。')}
        </div></details>
        <p class="fan-message" data-fan-message role="status" hidden></p>
        <footer class="toolbar toolbar-action-right fan-actions">
          <button class="primary" type="submit" data-fan-save>保存</button><button type="button" data-fan-cancel>取消</button>
        </footer>
      </form>`;
    }
    function contentHTML() {
      if (available === false) return '<p class="fan-unavailable" role="status">当前硬件不支持温度控制</p>';
      if (!confirmed) return `<p class="fan-unavailable" role="status">${readError ? safeText(readError) : '正在读取温控配置…'}</p>${readError ? '<button type="button" data-fan-retry>重试</button>' : ''}`;
      return `<section class="fan-status" aria-label="实时状态">
        <p class="fan-service" data-fan-service></p>
        <dl class="fan-temperatures">${[['cpu', 'CPU'], ['wifi', 'WiFi'], ['board', '主板']].map(([source, label]) =>
          `<div><dt>${label}</dt><dd data-fan-temperature="${source}">--</dd></div>`).join('')}</dl>
        <p class="fan-status-error" data-fan-status-error role="status" hidden></p>
      </section>${formHTML()}`;
    }
    function render() {
      return `<div class="fan-page" data-fan-page><div data-fan-content>${contentHTML()}</div><div class="fan-tooltip" id="fan-help-tooltip" role="tooltip" hidden></div></div>`;
    }
    function updateStatus() {
      const service = root?.querySelector('[data-fan-service]');
      if (!service) return;
      const output = Number.isFinite(status?.output_pwm) ? `${Math.round(status.output_pwm)}%` : '--';
      service.textContent = `${status ? (status.running ? '控制服务运行中' : '控制服务未运行') : '控制服务状态未知'} · 当前输出 PWM ${output}`;
      service.classList.toggle('is-running', status?.running === true);
      for (const source of ['cpu', 'wifi', 'board']) {
        const values = (status?.sensors || []).filter((sensor) => sensor.source === source && Number.isFinite(sensor.value)).map((sensor) => sensor.value);
        const value = values.length ? Math.max(...values) : status?.temperatures?.[source];
        root.querySelector(`[data-fan-temperature="${source}"]`).textContent = Number.isFinite(value) ? `${value.toFixed(1)} °C` : '--';
      }
      const error = root.querySelector('[data-fan-status-error]');
      error.textContent = [readError ? `状态读取失败：${readError}` : '', status?.error || ''].filter(Boolean).join(' · ');
      error.hidden = !error.textContent;
    }
    function update() {
      if (!root?.isConnected) return;
      const conditions = {
        manual: draft.mode === 'manual', curve: draft.mode === 'curve',
        linear: draft.mode === 'curve' && draft.curve_profile === 'linear',
        custom: draft.mode === 'curve' && draft.curve_profile === 'custom',
        cpu: draft.mode === 'curve' && draft.temp_source === 'cpu',
        single: draft.mode === 'curve' && draft.temp_source === 'cpu' && draft.cpu_statistic === 'single'
      };
      root.querySelectorAll('[data-fan-when]').forEach((node) => { node.hidden = !conditions[node.dataset.fanWhen]; });
      if (helpButton?.closest('[hidden]')) closeHelp();
      root.querySelectorAll('[data-fan-field]').forEach((control) => {
        control.disabled = saving || isReadonly() || available !== true || Boolean(control.closest('[hidden]'));
      });
      root.querySelector('[data-fan-form]')?.setAttribute('aria-busy', String(saving));
      const save = root.querySelector('[data-fan-save]');
      const cancel = root.querySelector('[data-fan-cancel]');
      if (save) {
        save.disabled = saving || isReadonly() || available !== true || !dirty();
        save.textContent = saving ? '保存中…' : '保存';
        cancel.disabled = saving || !dirty();
      }
      const note = root.querySelector('[data-fan-message]');
      if (note) {
        note.textContent = isReadonly() ? '当前账号为只读账号' : message;
        note.hidden = !note.textContent;
        note.classList.toggle('is-error', messageKind === 'error' && !isReadonly());
        note.setAttribute('role', messageKind === 'error' ? 'alert' : 'status');
      }
      updateStatus();
    }
    function writeControls() {
      root?.querySelectorAll('[data-fan-field]').forEach((control) => {
        const key = control.dataset.fanField;
        const point = /^curve-(\d)-(temperature|pwm)$/.exec(key);
        control.value = point ? draft.curve[Number(point[1])][point[2]] : draft[key];
        control.removeAttribute('aria-invalid');
        const output = root.querySelector(`[data-fan-output="${key}"]`);
        if (output) output.textContent = `${control.value}%`;
      });
    }
    function closeHelp() {
      helpButton?.setAttribute('aria-expanded', 'false');
      helpButton?.removeAttribute('aria-describedby');
      helpButton = null;
      helpPinned = false;
      const tooltip = root?.querySelector('[role="tooltip"]');
      if (tooltip) tooltip.hidden = true;
    }
    function openHelp(button, pinned = false) {
      closeHelp();
      helpButton = button;
      helpPinned = pinned;
      const tooltip = root.querySelector('[role="tooltip"]');
      tooltip.textContent = button.dataset.fanHelp;
      tooltip.hidden = false;
      button.setAttribute('aria-expanded', 'true');
      button.setAttribute('aria-describedby', tooltip.id);
      positionHelp();
    }
    function positionHelp() {
      if (!helpButton) return;
      const tooltip = root.querySelector('[role="tooltip"]');
      const anchor = helpButton.getBoundingClientRect();
      if (anchor.bottom < 0 || anchor.top > window.innerHeight) {
        closeHelp();
        return;
      }
      const box = tooltip.getBoundingClientRect();
      const left = Math.max(12, Math.min(anchor.right - box.width, window.innerWidth - box.width - 12));
      const top = anchor.bottom + box.height + 8 <= window.innerHeight ? anchor.bottom + 6 : Math.max(8, anchor.top - box.height - 6);
      tooltip.style.left = `${left}px`;
      tooltip.style.top = `${top}px`;
    }
    function inHelpArea(event) {
      return [helpButton, root.querySelector('[role="tooltip"]')].some((node) => {
        if (!node || node.hidden) return false;
        const box = node.getBoundingClientRect();
        return event.clientX >= box.left - 6 && event.clientX <= box.right + 6
          && event.clientY >= box.top - 6 && event.clientY <= box.bottom + 6;
      });
    }
    function mount(node) {
      unmount();
      root = node;
      events = new AbortController();
      const options = { signal: events.signal };
      root.addEventListener('input', (event) => {
        const control = event.target.closest('[data-fan-field]');
        if (!control || saving || isReadonly()) return;
        const key = control.dataset.fanField;
        const point = /^curve-(\d)-(temperature|pwm)$/.exec(key);
        const value = control.type === 'number' || control.type === 'range' ? (control.value === '' ? '' : Number(control.value)) : control.value;
        if (point) draft.curve[Number(point[1])][point[2]] = value;
        else draft[key] = value;
        control.removeAttribute('aria-invalid');
        const output = root.querySelector(`[data-fan-output="${key}"]`);
        if (output) output.textContent = `${value}%`;
        message = '';
        messageKind = '';
        update();
      }, options);
      root.addEventListener('submit', (event) => { event.preventDefault(); void save(); }, options);
      root.addEventListener('click', (event) => {
        const button = event.target.closest('button');
        if (!button) return;
        if (button.matches('[data-fan-help]')) {
          if (helpButton === button && helpPinned) closeHelp();
          else openHelp(button, true);
        }
        if (button.matches('[data-fan-retry]')) void refresh();
        if (button.matches('[data-fan-cancel]') && !saving && confirmed) {
          draft = clone(confirmed);
          message = '';
          messageKind = '';
          closeHelp();
          writeControls();
          update();
        }
      }, options);
      root.addEventListener('pointerover', (event) => {
        const button = event.target.closest('[data-fan-help]');
        if (button && !button.contains(event.relatedTarget) && !helpPinned) openHelp(button);
      }, options);
      root.addEventListener('pointerout', (event) => {
        if (!helpPinned && helpButton && event.target.closest('[data-fan-help]') === helpButton
          && !helpButton.contains(event.relatedTarget) && !inHelpArea(event)) closeHelp();
      }, options);
      document.addEventListener('pointermove', (event) => {
        if (helpButton && !helpPinned && !inHelpArea(event)) closeHelp();
      }, options);
      root.addEventListener('focusin', (event) => {
        if (event.target.matches('[data-fan-help]') && !helpPinned) openHelp(event.target);
      }, options);
      root.addEventListener('focusout', (event) => { if (event.target === helpButton && !helpPinned) closeHelp(); }, options);
      document.addEventListener('pointerdown', (event) => {
        if (helpButton && !helpButton.contains(event.target) && !root.querySelector('[role="tooltip"]').contains(event.target)) closeHelp();
      }, options);
      document.addEventListener('keydown', (event) => { if (event.key === 'Escape') closeHelp(); }, options);
      window.addEventListener('resize', positionHelp, options);
      document.addEventListener('scroll', positionHelp, { ...options, capture: true });
      update();
    }
    function unmount() {
      closeHelp();
      events?.abort();
      events = null;
      root = null;
    }
    function rebuildContent() {
      if (!root?.isConnected) return;
      closeHelp();
      root.querySelector('[data-fan-content]').innerHTML = contentHTML();
      update();
    }
    function accept(payload, syncDraft = false) {
      const hadConfig = Boolean(confirmed);
      const previousAvailability = available;
      const wasDirty = dirty();
      available = payload.available;
      status = payload.status || null;
      readError = '';
      if (available) {
        confirmed = configOnly(payload.config);
        // Status polls never replace a dirty draft or a focused/dragged control.
        if (syncDraft || (!wasDirty && !root?.contains(document.activeElement))) {
          draft = clone(confirmed);
          writeControls();
        }
      }
      if (previousAvailability !== available) onAvailability(available);
      if (previousAvailability !== available || (!hadConfig && confirmed)) rebuildContent();
      update();
    }
    async function refresh() {
      if (readRequest || saving) return;
      const version = generation;
      const request = apiJSON(endpoint);
      readRequest = request;
      try {
        const payload = checkedResponse(await request);
        if (version === generation) accept(payload);
      } catch (error) {
        if (version !== generation) return;
        readError = error.message || '温控状态暂时无法读取';
        if (!confirmed) rebuildContent();
        update();
      } finally {
        if (readRequest === request) readRequest = null;
      }
    }
    async function save() {
      if (saving || isReadonly() || available !== true || !dirty()) return;
      const invalid = validate(draft);
      if (invalid) {
        message = invalid.message;
        messageKind = 'error';
        const control = root.querySelector(`[data-fan-field="${invalid.key}"]`);
        if (control) {
          const advanced = control.closest('details');
          if (advanced) advanced.open = true;
          control.setAttribute('aria-invalid', 'true');
          control.focus();
        }
        update();
        return;
      }
      const expected = configOnly(clone(draft));
      const version = ++generation;
      saving = true;
      message = '';
      closeHelp();
      update();
      try {
        const mutation = checkedResponse(await apiJSON(endpoint, {
          method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(expected)
        }));
        const readback = checkedResponse(await apiJSON(endpoint));
        if (version !== generation) return;
        if (!mutation.available || !readback.available || !equal(mutation.config, expected) || !equal(readback.config, expected)) {
          accept(readback);
          throw new Error('保存响应与 API 回读不一致，配置尚未确认');
        }
        accept(readback, true);
        message = '配置已保存，运行状态以下次采样为准';
        messageKind = 'success';
        toast('温控配置已保存');
      } catch (error) {
        if (version !== generation) return;
        message = error.message || '温控配置保存失败';
        messageKind = 'error';
      } finally {
        if (version === generation) {
          saving = false;
          update();
        }
      }
    }
    function stop() {
      generation++;
      if (timer !== null) window.clearInterval(timer);
      timer = null;
      readRequest = null;
      saving = false;
      unmount();
    }
    function start() {
      stop();
      available = null;
      confirmed = null;
      status = null;
      draft = clone(defaults);
      readError = '';
      message = '';
      messageKind = '';
      onAvailability(null);
      void refresh();
      timer = window.setInterval(() => {
        if (root?.isConnected && available !== false && !document.hidden) void refresh();
      }, 3000);
    }
    return Object.freeze({ render, mount, unmount, update, start, stop,
      isAvailable: () => available, isMounted: () => Boolean(root?.isConnected) });
  }
  window.LyRouteGatewayFan = Object.freeze({ create });
}());
