# Online Users And 5 GHz Repair

This follow-up supersedes the earlier 5 GHz hardware-limit interpretation
in velo5x0-ge2-wifi-repair.md.

## Online User Count

The online-users API initially returned the GE2 client, but the production
traffic overview rendered zero. The bundled modules/overview.js read
dashboard.online_users, which the gateway dashboard does not provide.
The separately served gateway-overview.js already used online-user data;
testing that file alone did not cover the actual bundle.

The production module now counts the online-users response, including its
IPv4 address aliases and API envelope forms. It does not use a fabricated
dashboard fallback.

The VPP collector also built its LAN allowlist only from wired interface
configuration. Wi-Fi AP neighbors and NAT connections were excluded.
Enabled AP configuration now includes lywifi-ap and 192.168.89.0/24 in the
business LAN observation scope. Disabled APs, management-client mode, WAN
neighbors and internal DNS handoff traffic remain excluded.

The real desktop/mobile traffic overview and online-user API now agree on
three IPv4 endpoints: 192.168.88.120, 192.168.89.100 and 192.168.89.101.
These are IP identities, not three distinct people; the PC has wired and
wireless addresses.

## 5 GHz Root Cause

The device originally booted with a regulatory database whose signature
its custom kernel rejected. The ath10k PHY initialized in fallback country
99, with all 5 GHz channels marked no-IR. Changing the global country later
did not rebuild that PHY's initialized rules.

With the trusted upstream database selected, reinitializing ath10k_pci
restored signed per-PHY rules and exposed non-DFS 5 GHz AP channels.
This disproved the earlier assumption that hardware itself blocked 5 GHz.
Kernel country 98 was temporarily observed during rule intersection;
reloading the database and resubmitting CN restored explicit global CN.
The helper now performs one bounded reload/retry if country verification
initially fails; it never treats 98 as an unverified CN success.

Future hardware images select the trusted signed database during rootfs
assembly, before the wireless driver's first probe. No rootfs or ISO was
built during this hotfix. Existing-device alternatives selection is already
persistent. No EEPROM edit, firmware replacement, signature bypass or
unconditional no-IR removal was used.

The user had selected AU while troubleshooting. Only the country was
restored to CN for the confirmed China location. The existing password
secret was retained in all public API updates and controller restores.
The Windows profile was updated to that same user-selected password;
the earlier generated password was not written back to the router.

## Acceptance

- Public Wi-Fi API applied CN, 5 GHz, channel 149, 80 MHz.
- AP reports 5745 MHz, VPP business ready and active global CN.
- The PC and phone are associated on the real radio with 80 MHz VHT.
- Windows reports LyRoute, 802.11ac, channel 149; its observed 780 Mbps
  link rate is not an Internet throughput result.
- Baidu HTTPS through source 192.168.89.100 returned 200.
- Both Wi-Fi clients have real VPP NAT rewrites to WAN 192.168.1.221.
- Desktop 1440x1000 and mobile 390x844 UI checks passed: count matches
  the API, switching 2.4 GHz/5 GHz offers usable channels, draft cancel
  preserves the saved 149/80 MHz settings, and no page errors occurred.
- Runtime and rendered hostapd PSK were checked against the user's current
  password without printing the secret. Failed systemd units were zero.
- Public runtime apply runtime-58c8e4e34ab7dac6879e80ab05cb79c2 committed;
  5 GHz AP, CN, VPP readiness and all three online IPs survived it.
- Twenty helper tests, production overview count tests, focused gateway
  collector/online-user/Wi-Fi API tests and the daily source gate passed.

Fresh helper, controller and UI artifacts were sealed and deployed only
through scripts/hotfix-deploy.sh. Physical reboot, sustained throughput
and DFS AP operation were not tested.
