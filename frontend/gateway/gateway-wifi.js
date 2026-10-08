(function () {
  function create({ apiJSON, safeText, isReadonly, toast }) {
    let root = null, timer = null, config = null, status = null, dirty = false, busy = false, generation = 0;
    const escape = (value) => safeText(String(value ?? ''));
    function option(value, label, selected) {
      return `<option value="${escape(value)}" ${String(value) === String(selected) ? 'selected' : ''}>${escape(label)}</option>`;
    }
    function field(name, label, html) {
      return `<label class="wifi-field"><span>${escape(label)}</span>${html}</label>`;
    }
    function select(name, label, values) {
      return field(name, label, `<select name="${name}">${values.map(([value, text]) => option(value, text, config[name])).join('')}</select>`);
    }
    function channels() {
      return (status?.capabilities?.channels || []).filter((item) => item.band === config.band && !item.disabled &&
        (config.mode !== 'ap' || (!item.no_ir && !item.radar)));
    }
    function render() {
      return `<section data-wifi-page class="wifi-page"><div data-wifi-status role="status">读取无线状态...</div>
        <div data-wifi-error role="alert" hidden></div><div data-wifi-content></div>
        <section class="wifi-results"><header><h2>附近网络</h2><button type="button" data-wifi-scan title="扫描附近网络" aria-label="扫描附近网络">&#8635;</button></header><div data-wifi-networks></div></section>
        <section class="wifi-results"><h2>已连接客户端</h2><div data-wifi-stations></div></section></section>`;
    }
    function form() {
      if (!root || !config) return;
      const countries = [['', '未选择'], ['CN', '中国大陆'], ['US', '美国'], ['JP', '日本'],
        ['GB', '英国'], ['DE', '德国'], ['HK', '中国香港'], ['TW', '中国台湾'], ['AU', '澳大利亚'], ['SG', '新加坡']];
      if (config.country && !countries.some(([code]) => code === config.country)) countries.push([config.country, config.country]);
      const values = channels().map((item) => [item.channel, `${item.channel} (${item.frequency} MHz)`]);
      if (!values.some(([value]) => value === config.channel)) values.unshift([config.channel, `${config.channel} (当前监管域不可用)`]);
      root.querySelector('[data-wifi-content]').innerHTML = `<form data-wifi-form class="wifi-form">
        ${field('enabled', '无线电', `<input type="checkbox" name="enabled" ${config.enabled ? 'checked' : ''}>`)}
        ${select('mode', '工作模式', [['ap', '业务 AP'], ['client', '无线管理客户端']])}
        ${field('ssid', 'SSID', `<input name="ssid" maxlength="32" value="${escape(config.ssid)}" required autocomplete="off">`)}
        ${field('password', config.password_set ? '更换密码' : '密码', `<input name="password" type="password" autocomplete="new-password" minlength="8" maxlength="63" ${config.password_set ? '' : (config.enabled ? 'required' : '')}>`)}
        ${select('security', '安全模式', [['wpa2', 'WPA2-AES'], ['mixed', 'WPA2 / WPA3'], ['wpa3', 'WPA3-SAE']])}
        ${select('country', '监管区域', countries)}
        <div data-wifi-ap-fields class="wifi-ap-fields">
          ${select('band', '频段', [['2g', '2.4 GHz'], ['5g', '5 GHz']])}
          ${select('channel', '信道', values)}
          ${select('width', '带宽', (config.band === '2g' ? [20, 40] : [20, 40, 80]).map((value) => [value, `${value} MHz`]))}
          ${field('hidden', '隐藏 SSID', `<input name="hidden" type="checkbox" ${config.hidden ? 'checked' : ''}>`)}
          ${field('isolate', '客户端隔离', `<input name="isolate" type="checkbox" ${config.isolate ? 'checked' : ''}>`)}
          ${field('max_clients', '客户端上限', `<input name="max_clients" type="number" min="1" max="128" value="${config.max_clients}" required>`)}
        </div>
        <div class="wifi-network-state"><span>接入网络</span><strong data-wifi-network></strong><span>网关 / DNS</span><strong data-wifi-gateway></strong><span>业务转发</span><strong data-wifi-business></strong><span>生效监管域</span><strong data-wifi-regulatory></strong></div>
        <footer><button type="button" data-wifi-cancel>取消</button><button type="submit" data-wifi-save>保存</button></footer>
      </form>`;
      update();
    }
    function update() {
      if (!root?.isConnected) return;
      const names = { disabled: '无线关闭', starting: '启动中', ap_ready: 'AP 运行中', connected: '已连接' };
      root.querySelector('[data-wifi-status]').textContent = status
        ? `${names[status.state] || status.state} · ${status.interface} · ${status.addresses?.join(', ') || '无 IP 地址'}` : '无线服务不可用';
      root.querySelector('[data-wifi-ap-fields]')?.toggleAttribute('hidden', config?.mode !== 'ap');
      const businessNames = { disabled: '未启用', forwarding_ready: 'VPP 已就绪',
        waiting_for_dataplane: '等待业务配置', unavailable: '不可用', management_only: '仅管理' };
      const network = root.querySelector('[data-wifi-network]');
      if (network) {
        network.textContent = config?.mode === 'ap' ? '业务 LAN' : '独立管理网';
        root.querySelector('[data-wifi-gateway]').textContent = config?.mode === 'ap' ? '192.168.89.1' : '--';
        root.querySelector('[data-wifi-business]').textContent = businessNames[status?.business?.state] || '不可用';
        root.querySelector('[data-wifi-regulatory]').textContent = status?.regulatory?.active || '--';
      }
      root.querySelectorAll('[data-wifi-form] input, [data-wifi-form] select').forEach((node) => {
        node.disabled = busy || isReadonly() || !!node.closest('[hidden]');
      });
      const save = root.querySelector('[data-wifi-save]');
      if (save) {
        save.disabled = busy || isReadonly() || !dirty;
        save.textContent = busy ? '应用中...' : '保存';
        root.querySelector('[data-wifi-cancel]').disabled = busy || !dirty;
      }
      root.querySelector('[data-wifi-scan]').disabled = busy || isReadonly() || !status;
      const stations = status?.stations || [];
      root.querySelector('[data-wifi-stations]').innerHTML = stations.length
        ? `<div class="wifi-table-wrap"><table><thead><tr><th>MAC</th><th>信号</th><th>接收</th><th>发送</th></tr></thead><tbody>${stations.map((item) =>
          `<tr><td>${escape(item.mac)}</td><td>${escape(item.signal)}</td><td>${escape(item.rx_bitrate)}</td><td>${escape(item.tx_bitrate)}</td></tr>`).join('')}</tbody></table></div>` : '<p class="empty">暂无客户端</p>';
    }
    function error(message) {
      if (!root) return;
      const node = root.querySelector('[data-wifi-error]');
      node.textContent = message || '';
      node.hidden = !message;
    }
    async function load(initial = false) {
      const current = generation;
      try {
        const data = await apiJSON('/api/v1/wifi');
        if (!root || current !== generation) return;
        status = data.status || null;
        if (initial || !dirty) {
          const changed = JSON.stringify(config) !== JSON.stringify(data.config);
          config = { ...data.config };
          if (initial || changed) form();
        }
        error(data.error || '');
        update();
      } catch (failure) { if (current === generation) error(failure.message); }
    }
    async function save() {
      if (busy || isReadonly()) return;
      const current = generation;
      const payload = { ...config, password: root.querySelector('[name="password"]').value };
      busy = true;
      error('');
      update();
      try {
        const data = await apiJSON('/api/v1/wifi', { method: 'PUT', body: JSON.stringify(payload) });
        if (current !== generation || !root) return;
        config = data.config; status = data.status; dirty = false;
        form(); toast('无线配置已保存并应用');
      } catch (failure) { if (current === generation) error(failure.message); }
      finally { if (current === generation) { busy = false; update(); } }
    }
    async function scan() {
      if (busy || isReadonly()) return;
      const current = generation;
      busy = true; update(); error('');
      try {
        const data = await apiJSON('/api/v1/wifi/scan', { method: 'POST', body: '{}' });
        if (current !== generation || !root) return;
        const networks = data.networks || [];
        root.querySelector('[data-wifi-networks]').innerHTML = networks.length
          ? `<div class="wifi-table-wrap"><table><thead><tr><th>SSID</th><th>BSSID</th><th>信号</th><th>频率</th><th>安全</th></tr></thead><tbody>${networks.map((item) =>
            `<tr><td>${escape(item.ssid || '隐藏网络')}</td><td>${escape(item.bssid)}</td><td>${escape(item.signal)} dBm</td><td>${escape(item.frequency)} MHz</td><td>${escape(item.security)}</td></tr>`).join('')}</tbody></table></div>` : '<p class="empty">未发现网络</p>';
      } catch (failure) { if (current === generation) error(failure.message); }
      finally { if (current === generation) { busy = false; update(); } }
    }
    function mount(node) {
      unmount(); root = node;
      root.addEventListener('submit', (event) => { event.preventDefault(); void save(); });
      root.addEventListener('input', (event) => {
        const node = event.target;
        if (!node.name || busy || isReadonly() || !config) return;
        if (node.name !== 'password') config[node.name] = node.type === 'checkbox' ? node.checked
          : ['channel', 'width', 'max_clients'].includes(node.name) ? Number(node.value) : node.value;
        dirty = true; update();
      });
      root.addEventListener('change', (event) => {
        if (!['band', 'mode'].includes(event.target.name) || busy || isReadonly()) return;
        const password = root.querySelector('[name="password"]').value;
        if (config.band === '2g' && config.width === 80) config.width = 20;
        config.channel = channels()[0]?.channel || (config.band === '2g' ? 1 : 36);
        form();
        root.querySelector('[name="password"]').value = password;
      });
      root.addEventListener('click', (event) => {
        if (event.target.closest('[data-wifi-scan]')) void scan();
        if (event.target.closest('[data-wifi-cancel]') && !busy) { dirty = false; void load(true); }
      });
      void load(true);
      timer = setInterval(() => { if (!busy) void load(); }, 5000);
    }
    function unmount() {
      generation++;
      clearInterval(timer); timer = null; root = null; config = null; status = null; dirty = false; busy = false;
    }
    return { render, mount, unmount, isMounted: () => !!root?.isConnected, update };
  }
  window.LyRouteGatewayWiFi = { create };
})();
