package gateway

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"ly-route/backend/internal/httpapi"
)

type vppctlWANLinkRuntime struct {
	binary string
	run    gatewayVPPCTLRunner
}

type wanRuntimeAddress struct {
	prefix netip.Prefix
	table  int
}

type wanDHCPLease struct {
	state     string
	address   string
	installed bool
}

func (observer vppctlWANLinkRuntime) ObserveWANLinks(ctx context.Context, requests []httpapi.WANLinkRuntimeRequest) (map[string]httpapi.WANLinkRuntimeObservation, error) {
	addressOutput, err := observer.run(ctx, observer.binary, "show", "interface", "address")
	if err != nil {
		return nil, err
	}
	addresses, admin, err := parseWANRuntimeAddresses(addressOutput)
	if err != nil {
		return nil, err
	}
	needV4, needV6, needDHCP := false, false, false
	for _, request := range requests {
		if strings.HasSuffix(request.Mode, "6") {
			needV6 = true
		} else {
			needV4 = true
		}
		needDHCP = needDHCP || request.Mode == "dhcp4" || request.Mode == "dhcp"
	}
	routes := map[int]map[string]string{}
	routes6 := map[int]map[string]string{}
	for _, family := range []struct {
		needed bool
		name   string
		prefix string
		target *map[int]map[string]string
	}{{needV4, "ip", "0.0.0.0/0", &routes}, {needV6, "ip6", "::/0", &routes6}} {
		if !family.needed {
			continue
		}
		output, err := observer.run(ctx, observer.binary, "show", family.name, "fib", family.prefix)
		if err != nil {
			return nil, err
		}
		*family.target, err = parseWANRuntimeDefaultRoutes(output)
		if err != nil {
			return nil, err
		}
	}
	leases := map[string]wanDHCPLease{}
	if needDHCP {
		output, err := observer.run(ctx, observer.binary, "show", "dhcp", "client")
		if err != nil {
			return nil, err
		}
		leases = parseWANRuntimeDHCP(output)
	}
	counterOutput, counterErr := observer.run(ctx, observer.binary, "show", "interface")
	counters := map[string]map[string]any{}
	if counterErr == nil {
		for _, item := range parseAllVPPInterfaceTelemetry(counterOutput) {
			counters[telemetryString(item, "name")] = item
		}
	}
	sessionOutput, sessionErr := observer.run(ctx, observer.binary, "show", "nat44", "sessions")
	if sessionErr != nil || !strings.Contains(sessionOutput, "NAT44") {
		sessionOutput, sessionErr = observer.run(ctx, observer.binary, "show", "nat44", "ei", "sessions", "detail")
	}
	outsideSessions := map[string]int64{}
	if sessionErr == nil && strings.Contains(sessionOutput, "NAT44") {
		outsideSessions = countWANRuntimeSessions(sessionOutput)
	}
	result := map[string]httpapi.WANLinkRuntimeObservation{}
	for _, request := range requests {
		name := "lyroute-" + request.Interface
		current := httpapi.WANLinkRuntimeObservation{AdminUp: admin[name]}
		if item, found := counters[name]; found {
			rx, tx := telemetryInt64(item["rx_bytes"]), telemetryInt64(item["tx_bytes"])
			current.RxBytes, current.TxBytes = &rx, &tx
		}
		ipv6 := strings.HasSuffix(request.Mode, "6")
		fib := routes
		if ipv6 {
			fib = routes6
		}
		for _, address := range addresses[name] {
			if address.prefix.Addr().Is6() != ipv6 {
				continue
			}
			current.Address = address.prefix.String()
			current.Gateway = fib[address.table][name]
			current.RouteReady = current.Gateway != ""
			if current.RouteReady {
				break
			}
		}
		if request.Mode == "dhcp4" || request.Mode == "dhcp" {
			lease := leases[name]
			current.DHCPState = lease.state
			if lease.state != "DHCP_BOUND" || !lease.installed || lease.address != current.Address {
				current.Address = ""
				current.RouteReady = false
			}
		}
		if sessionErr == nil && strings.Contains(sessionOutput, "NAT44") {
			sessions := int64(0)
			if prefix, err := netip.ParsePrefix(current.Address); err == nil {
				sessions = outsideSessions[prefix.Addr().String()]
			}
			current.Sessions = &sessions
		}
		result[request.Interface] = current
	}
	return result, nil
}

func countWANRuntimeSessions(output string) map[string]int64 {
	counts := map[string]int64{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "o2i" {
			if address, err := netip.ParseAddr(fields[1]); err == nil {
				counts[address.String()]++
			}
		}
	}
	return counts
}

func parseWANRuntimeAddresses(output string) (map[string][]wanRuntimeAddress, map[string]bool, error) {
	addresses := map[string][]wanRuntimeAddress{}
	admin := map[string]bool{}
	name := ""
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) == 2 && (fields[1] == "(up):" || fields[1] == "(dn):") {
			name = fields[0]
			admin[name] = fields[1] == "(up):"
			continue
		}
		if len(fields) >= 2 && fields[0] == "L3" && name != "" {
			prefix, err := netip.ParsePrefix(fields[1])
			if err != nil {
				return nil, nil, fmt.Errorf("invalid live VPP address %q", fields[1])
			}
			if !prefix.Addr().IsGlobalUnicast() || prefix.Addr().IsLinkLocalUnicast() {
				continue
			}
			table := 0
			for index := 2; index+1 < len(fields); index++ {
				if fields[index] == "table-id" {
					table, err = strconv.Atoi(fields[index+1])
					if err != nil {
						return nil, nil, fmt.Errorf("invalid live VPP address table %q", fields[index+1])
					}
				}
			}
			addresses[name] = append(addresses[name], wanRuntimeAddress{prefix: prefix, table: table})
		}
	}
	if len(admin) == 0 {
		return nil, nil, fmt.Errorf("VPP interface address headers are unavailable")
	}
	return addresses, admin, nil
}

func parseWANRuntimeDefaultRoutes(output string) (map[int]map[string]string, error) {
	routes := map[int]map[string]string{}
	table, forwarding, defaultRoute := -1, false, false
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "ipv4-VRF:") || strings.HasPrefix(line, "ipv6-VRF:") {
			value := strings.SplitN(strings.SplitN(line, ":", 2)[1], ",", 2)[0]
			var err error
			table, err = strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid live VPP FIB table %q", value)
			}
			routes[table] = map[string]string{}
			forwarding, defaultRoute = false, false
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			if prefix, err := netip.ParsePrefix(fields[0]); err == nil {
				defaultRoute = prefix.Bits() == 0
				forwarding = false
			}
		}
		if strings.HasPrefix(line, "forwarding:") {
			forwarding = true
		}
		if table < 0 || !defaultRoute || !forwarding {
			continue
		}
		for index, field := range fields {
			if field == "via" && index+2 < len(fields) {
				gateway, err := netip.ParseAddr(fields[index+1])
				if err == nil && !gateway.IsUnspecified() {
					name := strings.TrimSuffix(fields[index+2], ":")
					routes[table][name] = gateway.String()
				}
			}
		}
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("VPP default-route FIB headers are unavailable")
	}
	return routes, nil
}

func parseWANRuntimeDHCP(output string) map[string]wanDHCPLease {
	leases := map[string]wanDHCPLease{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || !strings.HasPrefix(fields[0], "[") || fields[2] != "state" {
			continue
		}
		lease := wanDHCPLease{state: fields[3]}
		for index := 4; index+1 < len(fields); index++ {
			switch fields[index] {
			case "installed":
				lease.installed = fields[index+1] == "1"
			case "addr":
				lease.address = fields[index+1]
			}
		}
		leases[fields[1]] = lease
	}
	return leases
}
