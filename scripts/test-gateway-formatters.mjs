import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { runInNewContext } from 'node:vm';

const window = {};
runInNewContext(await readFile(new URL('../frontend/gateway/modules/overview.js', import.meta.url), 'utf8'), { window });
const overview = window.LyRouteGatewayOverview;
assert.equal(typeof overview.formatRate, 'function', 'interface and session tables need the shared rate formatter');
assert.equal(typeof overview.formatBytes, 'function', 'online users need the shared byte formatter');
for (const [value, expected] of [[undefined, '0 bps'], [0, '0 bps'], [1000, '1.00 Kbps'], [1000000, '1.00 Mbps'], [1000000000, '1.00 Gbps']]) {
  assert.equal(overview.formatRate(value), expected);
}
for (const [value, expected] of [[undefined, '0 B'], [0, '0 B'], [1024, '1.00 KiB'], [1048576, '1.00 MiB'], [1073741824, '1.00 GiB']]) {
  assert.equal(overview.formatBytes(value), expected);
}
console.log('PASS: interface, session and online-user formatters are exported');
