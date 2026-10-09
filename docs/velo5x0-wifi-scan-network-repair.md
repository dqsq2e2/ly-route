# Wi-Fi Scan And Configurable Business Subnet

Verified on 2026-10-09, Asia/Shanghai, on the physical Edge520 with kernel
6.18.54-velo5x0-r2 and the existing VPP native dataplane.

## Root Causes And Behavior

The enabled checkbox labeled “无线电” controls the Wi-Fi service. Its label is
now “启用 WiFi”; band and channel remain separate settings.

The scan helper ran `iw dev wlp1s0 scan passive` on the live AP interface.
The real driver rejected it with `Operation not supported (-95)`. The HTTP
runner also attached “previous configuration restored” to every failed
operation, including a scan that never changed configuration.

The radio accepts a temporary managed interface alongside its AP. Bringing
that interface up requires a distinct MAC address; reusing the AP MAC returned
`Name not unique on network`. The helper now creates an owned temporary
managed interface with a local MAC, excludes it from primary-radio discovery,
serializes scans and deletes the temporary interface on completion or failure.
It does not delete an existing interface occupying that name.

On this device, off-channel scans alongside the live AP returned
`scan aborted!` after about 20.5 seconds, even with exit status 0.
Current-channel passive scans succeeded. The helper therefore keeps the AP
running and explicitly reports `scope=current_channel` and the frequency.
The UI explains this scope; it does not claim an all-channel scan. Aborted
scans are failures, not successful empty results. Scan errors no longer claim
a configuration rollback, and only allowlisted public helper error codes are
returned, without child stderr or credentials.

The Wi-Fi business address, subnet and pool were fixed in the helper, and the
UI had no editor. The configuration now includes `ap_cidr`, `dhcp_pool_start`
and `dhcp_pool_end`, retaining the old defaults for existing records and
legacy updates. IPv4 gateway/pool validation excludes network and broadcast
addresses, reversed or out-of-subnet pools, and the gateway inside the pool.
Both Linux and VPP live interfaces are checked for overlap; native WAN
addresses exist only in VPP and must not be missed.

The selected subnet drives the VPP gateway, LCP address, Kea pool/router/DNS,
DNS return route and online-user NAT observation scope. Cleanup uses the
previous owned subnet from the service receipt. Changed subnets use separate
lease files so an old-network lease cannot be reused in the new pool.

## Management Gateway Meaning

The management API reports GE1 address 192.168.88.254/24 and configured
upstream gateway 192.168.88.1. The latter is for the appliance's management
network, not the DHCP router option.

The separate management Kea configuration on enp0s20f2 supplies router/DNS
192.168.88.254 and the management-address /32 on-link route. It does not supply
192.168.88.1 as the client gateway. The page now labels the field
“本机上游网关” and explains the distinction. This check verifies the setting's
meaning and generated DHCP options, not reachability of the configured .1.

## Verification

- All 27 helper tests, focused race-enabled Wi-Fi API and gateway
  telemetry/online-user tests, JavaScript syntax and daily source gate passed.
- The real authenticated scan API and desktop/mobile scan button returned
  CSU-Student at 5745 MHz. The AP/configuration remained unchanged by scanning
  and the temporary scan interface was removed.
- Real API updates overlapping management/GE2 192.168.88.0/24 and native WAN
  192.168.1.0/24 were rejected, retaining the prior configuration.
- A real API update to 192.168.90.1/24 with pool .90.40-.90.60 persisted and
  produced matching VPP, LCP, DNS-return-route and Kea readback. A synthetic
  TAP-side DHCP client received OFFER/ACK for 192.168.90.40 with gateway/DNS
  192.168.90.1. This verifies VPP/LCP/Kea, not physical Wi-Fi association or
  Internet traffic from a radio client on the custom subnet.
- The original 192.168.89.1/24 network and .89.100-.89.200 pool were restored.
  The AP remains CN, 5 GHz, channel 149, 80 MHz. The original hostapd hash and
  user-selected password are unchanged.
- Desktop 1440px and narrow 390px forms expose the new editable fields,
  cancel restores saved values, a real unchanged-network form save passes,
  scan scope is visible, and the management gateway label is correct.
  No horizontal document overflow or browser page errors occurred.
- Independently bound Windows GE2 source 192.168.88.120 resolved Baidu
  through .88.66 and returned certificate-verified HTTPS 200.
  GE1 SSH/HTTPS and both DHCP services remain active; no systemd units failed.

The computer's Wi-Fi connection was not changed. No appliance reboot,
rootfs/ISO build, all-channel AP scan or custom-subnet physical radio-client
acceptance was performed.

## Deployment

Fresh sealed artifacts were deployed only through scripts/hotfix-deploy.sh:

- Controller SHA-256:
  9f551807f8976330a40d2fc25ee09d42a860af504c1e045c29ef9a4618f94e5e.
- Wi-Fi helper SHA-256:
  5b24e753b338547a92d8ff86e843a0de99d9497598c935e1eb0194661cc88a03.
- Gateway app bundle SHA-256:
  d8ecdd3c00be3db58da522bbcfaee75e3bee211686d63fed17ad6a2af42ada8b.
