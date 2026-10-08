package httpapi

import (
	"context"
	"strings"
	"time"
)

type WANLinkRuntimeRequest struct {
	Interface string
	Mode      string
}

type WANLinkRuntimeObservation struct {
	Address    string
	Gateway    string
	AdminUp    bool
	RouteReady bool
	DHCPState  string
	RxBytes    *int64
	TxBytes    *int64
	Sessions   *int64
}

type WANLinkRuntimeObserver interface {
	ObserveWANLinks(context.Context, []WANLinkRuntimeRequest) (map[string]WANLinkRuntimeObservation, error)
}

func WithWANLinkRuntime(observer WANLinkRuntimeObserver) Option {
	return func(server *Server) { server.wanLinkRuntime = observer }
}

func (server *Server) decorateWANLinkRuntimeStates(ctx context.Context, items []map[string]any) []map[string]any {
	requests := make([]WANLinkRuntimeRequest, 0, len(items))
	for _, item := range items {
		mode := strings.ToLower(firstStringField(item, "type", "wan_type"))
		if mode == "" {
			if ipv4, ok := item["ipv4"].(map[string]any); ok {
				mode = stringField(ipv4, "mode")
			}
		}
		if mode == "pppoe" {
			continue
		}
		requests = append(requests, WANLinkRuntimeRequest{
			Interface: server.resolveInterfaceID(ctx, firstStringField(item, "interface_id", "system_name")),
			Mode:      mode,
		})
	}
	observations := map[string]WANLinkRuntimeObservation{}
	reason := "WAN runtime observer is not configured"
	if server.wanLinkRuntime != nil && len(requests) > 0 {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if current, err := server.wanLinkRuntime.ObserveWANLinks(readCtx, requests); err == nil {
			observations = current
			reason = ""
		} else {
			reason = err.Error()
		}
	}
	physicalLinks := map[string]string{}
	for _, item := range hostInterfaceInventory() {
		for _, key := range []string{"id", "system_name", "interface_id", "name"} {
			if name := stringField(item, key); name != "" {
				physicalLinks[name] = strings.ToLower(stringField(item, "link_state"))
			}
		}
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := cloneObject(item)
		if strings.EqualFold(firstStringField(item, "type", "wan_type"), "pppoe") {
			result = append(result, clone)
			continue
		}
		// Desired-state fields never prove a lease or a forwarding path.
		for _, key := range []string{"assigned_ipv4", "current_address", "runtime_address", "current_gateway", "lease", "gateway_reachable", "rx_bytes", "tx_bytes", "rx_bps", "tx_bps", "sessions"} {
			delete(clone, key)
		}
		clone["route_ready"] = false
		clone["operational_state"] = "unavailable"
		clone["operational_reason"] = reason
		id := server.resolveInterfaceID(ctx, firstStringField(item, "interface_id", "system_name"))
		if observed, ok := observations[id]; ok {
			link := physicalLinks[id]
			clone["current_address"] = observed.Address
			clone["current_gateway"] = observed.Gateway
			clone["route_ready"] = observed.RouteReady
			clone["dhcp_state"] = observed.DHCPState
			if observed.RxBytes != nil {
				clone["rx_bytes"] = *observed.RxBytes
			}
			if observed.TxBytes != nil {
				clone["tx_bytes"] = *observed.TxBytes
			}
			if observed.Sessions != nil {
				clone["sessions"] = *observed.Sessions
			}
			clone["physical_link_state"] = link
			clone["operational_state"] = "down"
			switch {
			case item["enabled"] == false:
				clone["operational_reason"] = "WAN is disabled"
			case link == "down":
				clone["operational_reason"] = "physical link is down"
			case !observed.AdminUp:
				clone["operational_reason"] = "VPP interface is down or absent"
			case observed.Address == "":
				clone["operational_reason"] = "no live VPP address"
			case !observed.RouteReady:
				clone["operational_reason"] = "no forwarding default route on this WAN"
			case link != "up":
				clone["operational_state"] = "unavailable"
				clone["operational_reason"] = "physical link state is unavailable"
			default:
				clone["operational_state"] = "up"
				clone["operational_reason"] = ""
			}
			clone["observed_at"] = server.now().UTC().Format(time.RFC3339Nano)
			clone["observation_source"] = "vpp_live"
		}
		if item["enabled"] == false {
			clone["operational_state"] = "down"
			clone["operational_reason"] = "WAN is disabled"
		}
		result = append(result, clone)
	}
	return result
}
