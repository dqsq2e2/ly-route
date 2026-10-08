# Management DHCP And Same-Prefix Protection

Verified on EDGE520, 2026-10-08, with kernel 6.18.54-velo5x0-r2 and Kea 2.2.

## Ownership

Business runtime plans own `kea-dhcp4-server.service` and
`/etc/kea/kea-dhcp4.conf`. They do not own the management DHCP service.
Management startup owns `kea-dhcp4-management-server.service`,
`/etc/kea/kea-dhcp4-management.conf`, `/run/kea-management`, and
`/var/lib/kea/management-leases4.csv`. Both processes bind only their
configured physical/LCP interfaces. Independent services are necessary
because Kea rejects two identical subnet prefixes in one configuration.

`management-network.py` generates the stable MAC/name network match and
management DHCP configuration. It keeps the existing business configuration
unchanged unless migrating an old management-only factory file. At startup,
management allocation excludes overlapping business pools and its router
and configured gateway. New images start business DHCP with no factory
management subnet; management DHCP is enabled independently.

The networkd source rule routes replies from the management IPv4 address
through table 19088 and its physical management interface. DHCP option 121
provides an on-link /32 route to the management address, so a dual-connected
client does not select the same-prefix business interface for management.
These are control-plane routes, not an alternative Linux business dataplane.

## Physical Verification

The user's GE1 address remains 192.168.88.254/24 and GE2 remains
192.168.88.66/24. Windows interface 4 receives 192.168.88.100 from .254;
interface 13 receives 192.168.88.120 from .66. Its DHCP-installed host route
to .254 selects interface 4.

The public authenticated runtime apply returned `committed`. Both DHCP
leases renewed afterward, and management stayed reachable. Networkd
reconfiguration and management DHCP restart were also tested.
Ordinary, unbound Windows ping to .254 passed 4/4; HTTPS returned 200.
Management source route lookup selects GE1, while the business source
lookup selects the GE2 LCP. VPP/WAN are still running, with the original WAN
DHCP address. No systemd unit is failed.

No addresses, passwords or firmware images were changed. No physical reboot
was performed; the service is enabled and networkd configuration is persisted.
This verification does not claim GE2, DSA or WiFi Internet acceptance.

## Checks And Recovery

- `python scripts/test-management-network.py`: 11 tests passed.
- `python scripts/test-velo5x0-hardware.py`: 18 tests passed.
- `sh scripts/test-firstboot-env-migration.sh`: passed.
- `bash scripts/dev-hotfix-check.sh ./internal/httpapi`: passed.
- Real-device Kea configuration validation and public apply: passed.

Fresh helper, service unit and firstboot files were sealed and deployed using
`scripts/hotfix-deploy.sh`. The source fingerprint is
`1a84fca16e80a6902650b4a8f9d726b957d87eefe155f5d2624ee177a21cbadd`.
The device's original configuration and SQLite backup are stored at
`/var/lib/ly-route/management-repair-backup-20261008T154223Z`.
