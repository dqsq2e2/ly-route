# Velo5x0 Runtime Failure Ledger

Updated: 2026-10-08. Physical appliance: kernel 6.18.54-velo5x0,
VPP 25.10 pinned at 744d3c7159ae130fb57f669a826ff1e45ed51836.

## WAN Status And Counters

The WAN collection returned only desired configuration. The UI required a live
address and route but received neither; it also displayed rate fields under
total-byte headings. The read-only WAN observer now reads VPP addresses,
bound DHCP leases, forwarding default routes, interface counters and NAT44
session counts. Physical carrier gates UP separately from desired/apply state.
Missing observations are unavailable, not fabricated DOWN or UP.

The public API and real browser initially verified SFP1 UP at
192.168.1.221/24 via 192.168.1.1. VPP public ICMP passed 3/3.
After recovery, the real browser again displayed UP, the full DHCP address,
byte totals and zero current NAT sessions. The earlier blank totals came from
rate fields, not byte counters.
The later failed GE2 apply removed SFP1 during rollback. Restoring VPP and the
underlay recreated its sockets but RX stayed stalled until the Linux SFP1 link
was cycled down/up. DHCP_BOUND and VPP public ICMP 3/3 then recovered. Warm
restart/igb RX reset behavior remains an open lifecycle issue.

## AF_XDP Lifecycle

2048-byte data buffers were insufficient for this igb zero-copy RX path.
3072-byte buffers allowed real DHCP reception. Duplicate creation previously
detached existing XDP; patch 0007 rejects the duplicate before cleanup.
Patch 0008 tracks RX ownership through fill-ring consumption and RX dequeue.
Two real GE2 create/delete cycles returned about 2048 buffers each, without
the earlier per-cycle leak. Duplicate SFP1 creation was rejected while its
XDP program remained attached and VPP public ICMP passed 3/3.

The first local patch-build verification was invalid: git apply inside an
ignored non-repository source subdirectory could skip the patch. The fresh
build was repeated with explicit patch-root application and content checks.
Only the rebuilt plugin with SHA-256
1f07a0c7658378bcd6483ab4fc14a58f6896c55b3b7543351591b41e76b13c90
provided the successful lifecycle observations above.

## GE2 DHCP Blockers

GE2 has the user's saved 192.168.88.66/24 LAN address. LAN1 has only a role
label, not a business address. Including that label in the native-path request
locked unrelated GE2 application. Role-only selection no longer requests a
forwarding attachment; the saved LAN1 role is preserved.

No DHCP server existed. A GE2 server was saved through the public API with
192.168.88.120-192.168.88.199, router 192.168.88.66 and a 43200-second lease.
The real apply then failed at LCP creation and rolled back. Direct reproduction
reported "linux-cp pair creation failed (-11)"; VPP logs showed that
/dev/vhost-net was absent. The installed kernel config has VHOST_NET disabled,
and modprobe vhost_net confirmed that the module does not exist.

The owning kernel fragment and package-module checks now require VHOST_NET,
VHOST, VHOST_IOTLB and built-in VHOST_TASK. Startup loads tun/vhost_net.
This requires a new kernel boot; existing-kernel GE2 DHCP and independent-PC
Internet acceptance are not passed. GE2's LAN subnet also overlaps GE1
management; retain the user's address pending an explicit topology change.
The new kernel release is 6.18.54-velo5x0-r2, with a separate package and
module directory so an update does not overwrite the running kernel.

## Source Checks

The focused WAN/runtime-interface Go tests, AF_XDP replay/native-selection
tests, UI formatter tests and kernel/glue package contract checks passed.
The package test now rejects a missing vhost_net module explicitly.
LAN session telemetry no longer fabricates zero. It counts observed NAT44
inside endpoints by the interface's live subnet; missing address/session
readback leaves the value unavailable. Focused telemetry tests passed and the
fresh controller was deployed with SHA-256
432fdd250c2d23b45fa327bb530deb5a2f4196ab6f331ba857eee2c464b01fcd.

CI run 37777198462 built and uploaded the r2 kernel successfully, but its
installer source-validation step failed. The complete source-validation
sequence passed locally. Direct log/artifact downloads hit TLS EOFs at the
Actions storage endpoint; the optional repair-evidence job retrieves the
original failure log through CI and preserves the verified kernel separately.
The original log was recovered by CI. Failure occurred in
test-vpp-native-selection.sh: its active-DPDK fixture was created by the
unprivileged runner, while active-dpdk-state.py intentionally accepts only
root-owned state files. The same failure was reproduced as nobody at the
dpdk-active assertion. CI now runs this isolated fixture with sudo, and the
fixture rejects a non-root invocation explicitly. The production ownership
guard is unchanged. A new full build is required to clear the failed run.

The broader httpapi suite timed out in
TestGatewayTelemetryIgnoresOlderCompletionAfterNewerSuccess: its fixture
waits for concurrent collection while the existing collector serializes calls.
The broader VPP suite also reported existing lifecycle/CLI expectation
failures outside the changed attach command. Those suites are not recorded as
passed. Focused checks are not a replacement for GE2 client acceptance.

## Remaining Scope

DSA LAN1-8 business forwarding and WiFi business Internet acceptance are not
complete. A visible interface, a role label, probe success or management-PC
Internet access does not establish business forwarding.
