# GE2 DNS And Wi-Fi Business Repair

Verified on the physical 5x0 device, kernel 6.18.54-velo5x0-r2.
Final client evidence is dated 2026-10-09 in Asia/Shanghai.

## GE2 Root Cause

The actual GE2 client had 192.168.88.120/24, router 192.168.88.66,
1 Gbps carrier, working public ICMP and working TCP to 223.5.5.5:443.
UDP DNS failed before repair; an incorrect DHCP router was not the first
failing layer in this reproduction.

The VPP DNS proxy defaulted to 127.0.0.53:53, while the packaged SmartDNS
listener uses 127.0.0.1:1053. The proxy now matches that listener. Dedicated
source-route ports and fail-closed source-policy behavior are preserved.

Separately, the device had no persisted default DNS policy or usable
VPP-backed upstream. Its fallback blocked unmatched names. The public API
now persists a direct SmartDNS policy with 223.5.5.5/223.6.6.6 over
wan-enp4s0f0-ipv4. The generated DNS handoff uses a dedicated VPP TAP,
Linux policy table and NAT inside role; it does not send queries through
the unreachable Linux management default gateway.

## Wi-Fi Business Path

AP traffic uses this path:

```text
wlp1s0 -> lywifi-br -> lywifi-data -> VPP lywifi-ap -> NAT44 -> SFP1
```

The bridge is an L2 handoff, not Linux routed forwarding. VPP owns the
192.168.89.1/24 business gateway. A separate LCP interface, lywifi-host,
serves Kea DHCP with 192.168.89.100-192.168.89.200 and gateway/DNS
192.168.89.1. Kea has its own lease, PID and lock paths.

The helper owns its TAP, bridge, LCP pair and NAT/DNS features, rejects
unowned name collisions, reconciles after business apply, and cleans up
only its own resources. The existing wireless client mode remains an
independent management client without installing a default route.

## Regulatory Database

The API and hostapd originally requested CN, but the kernel stayed at
global country 00. Kernel logs reported a missing/invalid regdb signature.
The installed wireless-regdb alternatives defaulted to Debian's signed
database, while the custom kernel has CFG80211_REQUIRE_SIGNED_REGDB=y and
CFG80211_USE_KERNEL_REGDB_KEYS=y.

The helper now selects the packaged upstream database/signature pair with
update-alternatives, reloads it when selection changes, and waits for the
requested global country to become active. Missing signatures and an
unchanged world domain fail activation. Signature checking is not disabled.
The device's README.Debian documents this exact custom-kernel requirement.

Final global readback is CN. The radio retains its separate country 99
hardware/driver restrictions; its 5 GHz channels still report no-IR.
The implementation preserves those constraints and does not unlock them.
The UI displays the observed global country separately from saved settings.

## Acceptance

- GE2: explicitly bound source 192.168.88.120 on Windows interface 13.
- Wi-Fi: explicitly bound source 192.168.89.100 on Windows interface 6.
- Both: gateway/public ICMP, three UDP DNS targets, TCP DNS and Baidu
  HTTPS 200 passed. Intercepted DNS targets do not prove direct upstream
  reachability to each target; the configured upstreams are AliDNS.
- Ordinary, non-overridden Baidu HTTPS also passed over LyRoute Wi-Fi.
- With Wi-Fi disconnected, Windows automatically selected GE2's default
  route via 192.168.88.66; unbound, non-overridden Baidu HTTPS returned 200
  with local_ip=192.168.88.120. LyRoute Wi-Fi was reconnected afterward.
- Identical Wi-Fi save and public runtime apply passed; final transaction
  runtime-89c2af2ad6e060d05942a060a7c72afc committed with business ready/CN.
- The final Wi-Fi service restart restored AP/business ready/CN in 4.297 s;
  the Windows client reconnected afterward.
- Management GE1 ICMP passed 2/2 and failed systemd units were zero.
- Desktop 1440x1000 and mobile 390x844 UI passed, with the real station
  visible, no form overflow and no browser page errors.
- All 18 Wi-Fi helper regressions, focused Go Wi-Fi tests, the DNS proxy
  source-routing/fail-closed tests and daily source gate passed.

Only sealed hotfixes were deployed through scripts/hotfix-deploy.sh.
Final helper SHA-256:
7530e83934c844371728498d18a7d7a034363b515eadefa3b9b7126847a0dba8.
DNS proxy SHA-256:
cbeb5c5c5367b4b5c99fbb1b90f3f12e6de06a57537765834d4dbba2f8ca8ae2.

No physical reboot, 5 GHz AP, sustained throughput or DSA LAN1-8 acceptance
was performed. 1.1.1.1:443 still times out on both clients; this particular
destination remains unresolved. AliDNS HTTPS returned 400 without a DNS
query payload, proving TLS/HTTP reachability, not successful DoH resolution.
No claim is made that every Internet destination is reachable.

## Subsequent Repair

The earlier 5 GHz hardware-limit interpretation was superseded by driver
reinitialization with the trusted regulatory database. Non-DFS 5 GHz AP,
80 MHz client association and HTTPS now pass. See
velo5x0-online-users-wifi5g-repair.md for the root cause, online-user count
fix and newer acceptance scope.
