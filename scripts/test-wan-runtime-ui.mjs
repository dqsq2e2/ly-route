import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const source = readFileSync(new URL('../frontend/gateway/app.js', import.meta.url), 'utf8');
const start = source.indexOf('function wanRuntimeState(');
const end = source.indexOf('\nfunction userProxyEgressVisible', start);
assert.ok(start >= 0 && end > start);
const context = vm.createContext({
  state: { controlPlane: { telemetry: { interfaces: { items: [{ id: 'wan0', link_state: 'up' }] } } } },
  envelopeItems: (value) => value?.items || [],
  displayValue: (...values) => values.find((value) => value !== undefined && value !== null && value !== '') || '',
});
vm.runInContext(source.slice(start, end), context);
const desired = { type: 'dhcp4', interface_id: 'wan0', runtime_state: 'desired_not_applied' };
assert.equal(context.wanRuntimeState({ ...desired, operational_state: 'up', current_address: '192.168.1.221/24' }).up, true);
assert.equal(context.wanRuntimeState({ ...desired, operational_state: 'down', current_address: '192.168.1.221/24' }).up, false);
assert.equal(context.wanRuntimeState({ ...desired, operational_state: 'unavailable' }).unavailable, true);
assert.equal(context.wanRuntimeState({ ...desired, operational_state: 'up', enabled: false }).up, false);
assert.equal(context.wanRuntimeState({ type: 'pppoe' }, { state: 'connected', route_ready: false, assigned_ipv4: '192.0.2.2' }).up, false);
assert.equal(context.wanRuntimeState({ type: 'pppoe' }, { state: 'connected', route_ready: true, assigned_ipv4: '192.0.2.2' }).up, true);
assert.ok(source.includes("'线路状态'"));
assert.ok(!source.includes("'逻辑状态'"));
const totalStart = source.indexOf('function interfaceTrafficTotal(');
const totalEnd = source.indexOf('\nfunction interfaceMembersText', totalStart);
context.gatewayOverview = { formatBytes: (value) => `${value} B` };
vm.runInContext(source.slice(totalStart, totalEnd), context);
assert.equal(context.interfaceTrafficTotal(0), '0 B');
assert.equal(context.interfaceTrafficTotal(1024), '1024 B');
assert.equal(context.interfaceTrafficTotal(undefined), '--');
assert.equal(context.interfaceTrafficTotal(-1), '--');
console.log('WAN live runtime UI regression passed');
