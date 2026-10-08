import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const context = { window: {} };
vm.runInNewContext(readFileSync(new URL("../frontend/gateway/modules/overview.js", import.meta.url), "utf8"), context);
const render = context.window.LyRouteGatewayOverview.renderTraffic;
const options = { dashboard: { online_users: 99 }, trend: {}, hidden: new Set(), escape: String };
const label = "\u5728\u7ebf\u7528\u6237";
function count(payload) {
  const html = render({ ...options, onlineUsers: payload });
  const match = html.match(new RegExp(`<span>${label}</span><strong>(\\d+)</strong>`));
  assert.ok(match, "online-user metric missing");
  return Number(match[1]);
}
const clients = [{ ip: "192.168.88.120" }, { ip_address: "192.168.89.101" }, { ip: "fe80::1" }];
for (const payload of [clients, { items: clients }, { data: clients }, { data: { items: clients } }]) {
  assert.equal(count(payload), 2, "count must use online-user readback, not dashboard fallback");
}
assert.equal(count({ data: { items: [] } }), 0, "an empty observed user list must count as zero");
console.log("Gateway traffic overview online-user count regressions passed");
