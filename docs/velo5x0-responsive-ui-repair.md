# Gateway Narrow-Screen Layout Repair

## Root Cause

The production gateway loads styles.css before commercial.css. The base
stylesheet switched app-shell to block layout below 900px, but the later
theme restored a desktop grid with a 220px sidebar track. The sidebar was
fixed-position or hidden, so automatic grid placement assigned the main
content to that first track and left the second track empty.

On the real device at 390x844, the NIC page's main element was only 220px
wide and its page card was 184px wide. System overview, traffic overview,
DHCP and other pages shared the same failure. Existing Wi-Fi, LAN/WAN and
fan page-specific classes masked it for those three routes.

Mobile table rules had a second cascade problem: later desktop minimum
widths (980/1040px) and high-specificity fixed row and child heights
survived the card layout. Long values could wrap beyond fixed-height
cells, and some renderers did not retain column labels on mobile.
The theme also used a 60px grid header while an earlier rule fixed the
header itself at 54px.

## Changes

- The last-loaded gateway theme owns the common mobile single-column
  shell, explicit main grid placement, header height and overlay sidebar.
- Removed page-specific shell classes and inline sidebar display state.
  CSS now restores the desktop sidebar after a narrow-to-wide resize.
- All direct list tables below 720px release desktop width, row and
  child height limits. Long values wrap, desktop filler rows disappear,
  and labels/actions retain aligned grid tracks.
- Added mobile data-label attributes to telemetry, configuration, DHCP,
  object, log and system-user rows without changing mutation behavior.
- Updated frontend asset query versions to invalidate existing caches.

## Verification

scripts/test-gateway-responsive-ui.mjs checks a freshly built production
controller-shell bundle with the existing local API fixture. Its live mode
checks the installed device without mocking the API or changing configuration.

Both modes passed all 19 menu pages at widths 320, 390, 600, 720, 800, 900,
901 and 1440px (844px height). Checks cover main/card width, header alignment,
contained horizontal scrolling, mobile labels and values, absence of clipped
cells and filler rows, real menu opening, Escape/navigation closing, and
narrow-to-desktop resizing. No browser exceptions occurred.

Actual-device NIC, Wi-Fi, DHCP and temperature screenshots were inspected.
At 390px the NIC main now measures 390px and the page card 376px.
The daily source gate, profile isolation, formatter and online-user count
regressions also passed.

Only four fresh frontend artifacts were sealed and installed through
scripts/hotfix-deploy.sh. Their device SHA-256 values are:

```text
styles.css     0e1729ad474615c28bfe6e64dee4b5390e22520737e6a0b7f8819546bce33b56
commercial.css ce6634bf7b4f4b636d0f561751ac0fdd0f832cde269bd060fdf7b5829a3590b0
app.js         e05c0a2311373aa0f3361fa5390a8f1b987c79c4e6af264a42ee7a8bcd32f7b8
index.html     d99c66a6d6888a93ffdbb00cf6eebe3cc6a7dbd25200f9b40f8ab358d0352504
```

nginx, control API, VPP and Wi-Fi services remained active. Runtime Wi-Fi
configuration and hostapd file hashes were unchanged. The user's current
password was verified against both the runtime secret and derived hostapd
PSK without printing it; CN, 5 GHz, channel 149 and 80 MHz were preserved.
GE2-bound HTTPS to Baidu returned 200 from 192.168.88.120.
Wi-Fi client HTTPS was not passed in this run: Windows reported its WLAN
interface disconnected, although it retained its previous IP. No client
reconnection, network configuration change, runtime apply, backend deployment,
rootfs/ISO build or reboot was performed.

Example fixture invocation after a fresh build:

```sh
bash scripts/build-controller-shell.sh --product gateway --out dist/verification/responsive-ui-fresh
node scripts/test-gateway-responsive-ui.mjs --bundle dist/verification/responsive-ui-fresh
```

Set GATEWAY_UI_PLAYWRIGHT_MODULE when Playwright is installed outside the
local node resolution path. Live mode uses GATEWAY_UI_URL,
GATEWAY_UI_PASSWORD and optionally GATEWAY_UI_USERNAME; screenshots and
layout measurements are written when GATEWAY_UI_EVIDENCE_DIR is set.
