# Management Gateway Inspection and Clear-Save Repair

Verified on the live appliance on 2026-10-09.

## Reachability and Gateway Meaning

GE1 is the exclusive kernel management interface, enp0s20f2, with
192.168.88.254/24. Its current Linux default route points to 192.168.88.1.
That address is an installer/firstboot fallback, not a discovered router;
the GE1 neighbor entry was unresolved and a public ICMP probe explicitly
sourced from 192.168.88.254 failed.

192.168.1.1 is the WAN gateway on SFP1's 192.168.1.0/24 network. It is not an
on-link next hop on GE1, so the management editor's same-subnet validation
remains appropriate.

192.168.88.66 is this appliance's own GE2/LCP address. The live Linux route
lookup from 192.168.88.254 to that address returns a local route through lo.
GE1 is absent from the VPP NAT interfaces; GE2 and Wi-Fi are inside, and
SFP1 is outside. Entering .88.66 in the management gateway field does not
provide a management-system-to-VPP egress path. Management access works,
but management Internet egress remains unconfigured. A PC using GE1 for
Internet would also need an explicit management/LAN forwarding design.

The management DHCP service advertises router/DNS 192.168.88.254 to clients.
This client router option is separate from the appliance's upstream setting.

## Clear-Save Root Cause and Change

The authenticated management save handler previously updated `gateway` only
when the submitted string was nonempty. Clearing the real form therefore
retained the old .88.1 value. This was reproduced through the live API and
in a failing focused regression test.

The handler now distinguishes an omitted field from a submitted empty value:
omission preserves the stored gateway; an empty or whitespace-only string
clears it. Non-string values are rejected. Off-subnet gateway validation
is retained. The UI explains the direct-PC and upstream-router cases and
shows that the field may be left empty.

## Verification and Deployment

Race-enabled focused management API tests, JavaScript syntax and
scripts/dev-hotfix-check.sh ./internal/httpapi passed before building fresh
controller and UI artifacts. Both were sealed and deployed only through
scripts/hotfix-deploy.sh.

- Controller SHA-256:
  1c020e996cc040989e900da8e7c4d730508e069150f12178ca0268c017f49005.
- Gateway app SHA-256:
  520939c3f829635e28be28f7c3d293f9d58d7430c4612b5a238b38c6cb9208e4.

The real 390px browser form saved an empty gateway and retained it after
reload. Authenticated PATCH/GET checks also verified omission, whitespace
clearing and off-subnet rejection without modifying the saved value on
failure. There was no horizontal overflow or browser page error.

The original desired gateway .88.1 was restored after the verification.
Management saves report `desired_not_applied`; no runtime apply or network
rewiring was performed. Clearing the desired setting does not itself remove
the live default route or create Internet connectivity.

The deployed hashes matched, all six checked services were active, no
systemd units failed, and VPP retained PID 114106. An independently
interface/source-bound Windows GE2 client resolved Baidu through .88.66
and returned certificate-verified HTTPS 200. Wi-Fi remained CN, channel149,
192.168.89.1/24 with the user's password unchanged. The PC's Wi-Fi connection
was not modified.

Evidence: dist/verification/management-egress-inspection.jsonl,
dist/verification/management-gateway-runtime.jsonl,
dist/verification/ge2-client-acceptance.json, and the
management-gateway-clear browser evidence directory.
