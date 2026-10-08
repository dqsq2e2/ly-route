# 5x0 Wireless And Dataplane Status

Checked on 2026-10-08. These are bounded observations, not a throughput
qualification or an assertion that all WiFi/LAN features are complete.

## Wireless

The QCA988x radio enumerates as `wlp1s0`, with AP and managed modes, 2.4/5 GHz,
and one concurrent operating channel. Wireless configuration is separate from
the twelve Ethernet jacks.

The initial integration implements a disabled-by-default, password-protected
management AP and a wireless management client. AP clients receive addresses
on the `192.168.89.0/24` management subnet. Client DHCP does not install a global
route or replace system DNS. This is not a business LAN/WAN ingress. The helper
does not configure Linux NAT, but the existing global IPv4 forwarding setting
is enabled; forwarding isolation must not be inferred from the absence of NAT.
VPP business integration remains outstanding.

Credentials use the existing encrypted secret store. Runtime files live in
`/run` with private permissions. Configuration/service failure rolls back the
previous document, secret and radio service. The controller restores saved
radio settings on startup.

The previous ImmortalWrt adaptation used ath10k-ct and firmware
`10.1-ct-8x-__fW-023-23ea9f8e`, advertising `get-temp-CT`. Its historical hwmon
reading was 37 C. The original Ly kernel used upstream ath10k and firmware
`10.2.4-1.0-00047`, returning ENETDOWN with the radio off and -15 C after
a passive scan. The current indoor hardware profile rejects that negative
reading for fan control and explicitly reports its availability.
No offset, substitute sensor or invented WiFi temperature is used.

The kernel patch includes a bounded CT temperature ABI port, guarded by the
firmware's feature bit 44. QCA988x on EDGE520/EDGE540 automatically prefers
the verified `firmware-ct.bin`; unsupported CT feature bits are ignored.
The rootfs profile downloads the pinned CT community firmware independently
and verifies its SHA-256. A fresh module built against the exact running
6.18.54 kernel was hot-deployed and loaded without a reboot. Its live firmware
version is `10.1-ct-8x-__fW-023-23ea9f8e`, CRC32 `42c82ae5`.

Passive scan and receive-only monitor mode still initially returned -15 C.
A temporary password-protected WPA2 AP on the currently permitted world-domain
channel 1 reached ENABLED and produced six real hwmon readings at three-second
intervals: 34, 36, 36, 37, 37 and 38 C. The test restored the previous disabled
radio configuration. This establishes the physical temperature operation in
active AP mode; it does not establish business traffic forwarding.

After the final driver reload, a second AP test recorded fifteen consecutive
readings between 34 and 40 C. The actual fan API reported the WiFi sensor as
ready, and the desktop/mobile temperature-control pages displayed 38.0 C.
Saving the WiFi temperature source through the UI changed the running fan
service's effective temperature to 39 C with no error. The WiFi API also
returned a ready numeric sensor. The test restored the fan settings and
removed the temporary AP. Reapplying a disabled configuration now always
stops the radio service and clears its network state, even when the saved
configuration is unchanged but runtime state has drifted.
AP association, WPA3 and DHCP require real-client acceptance after the device's
actual regulatory country is selected.

## Wired Dataplane

VPP 25.10 allocated RX queue structures for TX-only UMEMs and attempted to
refill/wake them on admin-up. Their RX descriptor is -1, causing the observed
`Bad file descriptor`. The repository patch bounds refill to `rxq_num`.
Creation uses all available RX queues as a compatible runtime workaround.
Semantic readback and capability probing reject device-error output.

The two SFP attachments were recreated with zero-copy and no device-error
output. GE2 successfully created a zero-copy probe. Older 5x0 installer
inventories omit GE2; runtime discovery now includes direct external PCI
jacks while excluding the management jack, DSA CPU uplinks and wireless.

LAN1 returned `Operation not supported` for direct AF_XDP zero-copy. LAN1-8
are DSA child interfaces, not independent PCI NICs. A switch-tag-aware native
VPP adapter is still required. Neither binding the CPU uplinks to DPDK nor
renaming AF_PACKET as a production path solves this.

Live native-readiness probing passes with zero-copy/admin-up evidence for
both SFP ports and GE2. Probe names fit VPP's 32-character output column and
netdev readback handles VPP CRLF output without creating a second socket.

No GE2/SFP/LAN independent physical packet-forwarding acceptance has passed:
the data jacks had no carrier during these checks. Interface creation and
driver flags are not packet-flow or performance acceptance.

## Idle CPU

Three live `/proc/stat` delta samples measured 53.07-53.38 percent total CPU.
Each VPP worker used approximately one full core on the four-core C2558.
AF_XDP and the session queue were polling while packet vectors remained zero.
The dashboard's approximately 54 percent is therefore real CPU time, not a
load-average conversion or WiFi/fan-control load. An adaptive RX-mode trial
was rejected by the existing interfaces' syscall-lock workaround, leaving
their polling mode unchanged. No worker count, CPU accounting or packet-path
performance setting was changed to hide that usage.

The former dashboard sampler covered only 200 ms immediately after each
request, sometimes showing a real but short 95-100 percent refresh-time peak.
The hot-deployed sampler now covers the full normal refresh interval and
shares results across concurrent requests. WiFi, fan and system-overview pages
also refresh only their applicable data. CPU values remain actual `/proc/stat`
deltas, with no subtraction of VPP time or artificial cap.
