# CPU Sampling: Original Branch Semantics

## Final Scope

The user requested restoring the original CPU sampling algorithm and keeping
the existing interface text unchanged. The final implementation matches
origin/main at d95c8a5:

```text
total = sum(all aggregate /proc/stat CPU fields)
idle = idle + iowait
cpu_busy_percent = round2(100 * (total - idle) / total)
```

This is the cumulative average since boot, not recent instantaneous usage.
There is no 200ms sample, request-driven interval, background sampling timer,
extra CPU sample metadata, new label, cap or display smoothing.

## Investigation

The earlier 5x0 sampler used interval deltas but still reset to a 200ms
measurement on first access or after more than 30 seconds without requests.
The real device's first API result was 70.89%, compared with an independent
5-second result of 54.21%. Subsequent API refreshes were about 54%.

A temporary independent 5-second sampler confirmed that some additional
page-load peaks were real: API and independent counters differed by at most
0.38 percentage points, including a 97.04% window. The original branch's
long-term average hides the magnitude of short peaks by definition; restoring
that algorithm does not prove the instantaneous load was reduced.
The VPP workers vpp_wk_0 and vpp_wk_1 each consume about one CPU core.
Their configuration, process and business forwarding were not changed.

That temporary sampler was removed following the user's explicit preference.
Only system_cpu.go and its focused tests own the final source change.

## Checks

- Regression tests match the original branch's complete aggregate row,
  including its handling of additional CPU fields.
- A synthetic 95% recent burst produces the expected cumulative 50.88%
  against a prior 50% history, rather than replacing the displayed average.
- Malformed, incomplete and all-zero counters remain unavailable.
- Focused CPU/dashboard/health/online-user/Wi-Fi tests passed with the race
  detector; the daily source gate compile-checked httpapi and gateway.
- Only a fresh sealed controller artifact is deployed through
  scripts/hotfix-deploy.sh. No rootfs/ISO build, reboot or runtime apply.

## Live Verification: 2026-10-09

- Final controller SHA-256:
  `a9e165835be27ecf217a30ba1f09b4ff7e27ad0842e430dea113a07398c759d5`.
  The installed binary matches the sealed artifact. Source fingerprint:
  `e7eeff02112faa74b9d6479edfa65d3c9549a1409b36011ef4d6d7dfe6609f85`.
- Ten consecutive dashboard requests and another after 35 seconds without
  requests all returned 54.45%, matching the original formula independently
  read from /proc/stat immediately before and after each request.
  Request durations were 0.063 to 0.247 seconds. No sampling metadata remains.
- Playwright checked 1440px desktop and 390px mobile viewports against the
  deployed device. Both displayed 54%, matching the rounded API value, with
  unchanged CPU usage text and no additional average label. The mobile body
  occupies the full 390px width; no page errors were reported.
- ly-route-control-api, ly-route-wifi and vpp are active. A GE2 client request
  bound to 192.168.88.120 received HTTPS 200 from www.baidu.com.
- Wi-Fi configuration and hostapd hashes remain unchanged:
  `8ce30a3b61642308500aa4df8562d5872540469bdea145ec76ac1a0f8f9b2c37`
  and `6e82ecbabffde9eb718b82aee2a4c1431a200e8425d1b9982981821f113af2b0`.
  The stored password remains the user's selected value, and hostapd's
  derived PSK matches it. No Wi-Fi client reconnect or Wi-Fi internet test
  was performed during this CPU-only repair.
