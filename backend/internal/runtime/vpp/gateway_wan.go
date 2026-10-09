package vpp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"ly-route/backend/internal/runtime/nat"
)

type GatewayNATEgress struct {
	Inside       []string     `json:"inside"`
	Outside      string       `json:"outside"`
	Behavior     nat.Behavior `json:"behavior"`
	RetainInside []string     `json:"-"`
}

func gatewayDHCPClientPresent(output, name string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 6 && strings.HasPrefix(fields[0], "[") && fields[1] == name && fields[2] == "state" && strings.HasPrefix(fields[3], "DHCP_") && fields[4] == "installed" {
			return true
		}
	}
	return false
}

func (channel vppctlChannel) doGatewayDHCPClient(ctx context.Context, operation Operation, assignment AddressAssignment) (Reply, error) {
	if strings.HasSuffix(operation.Name, ".rollback-delete") || strings.HasSuffix(operation.Name, ".reconcile-delete") {
		return channel.doCommands(ctx, operation)
	}
	results, err := channel.runServiceChainCommands(ctx, operation, "show dhcp client")
	if err != nil {
		return Reply{}, err
	}
	commands := []string{fmt.Sprintf("set interface state %s up", assignment.VPPInterface)}
	if !gatewayDHCPClientPresent(resultStdoutLast(results, "show dhcp client"), assignment.VPPInterface) {
		for _, address := range assignment.RemoveCIDRs {
			commands = append(commands, fmt.Sprintf("?set interface ip address del %s %s", assignment.VPPInterface, address))
		}
		commands = append(commands, fmt.Sprintf("set dhcp client intfc %s", assignment.VPPInterface))
	}
	commands = append(commands, "show dhcp client", "show interface address "+assignment.VPPInterface)
	applied, err := channel.runServiceChainCommands(ctx, operation, commands...)
	if err != nil {
		return Reply{}, err
	}
	results = append(results, applied...)
	if err := verifySupplementalOperation(operation, results); err != nil {
		return Reply{}, err
	}
	return routePolicyLifecycleReply(operation, results), nil
}

func gatewayNATEgressOperations(plan Plan) []Operation {
	var inside []string
	for _, assignment := range plan.AddressAssignments {
		if assignment.Role == "lan" && strings.TrimSpace(assignment.CIDR) != "" {
			inside = append(inside, assignment.VPPInterface)
		}
	}
	if len(inside) == 0 {
		return nil
	}
	var operations []Operation
	for _, assignment := range plan.AddressAssignments {
		if assignment.Role != "wan" || !assignment.NAT {
			continue
		}
		payload := GatewayNATEgress{Inside: inside, Outside: assignment.VPPInterface, Behavior: plan.NAT.Behavior}
		operations = append(operations, Operation{Name: "vpp.nat44.egress", RequestID: plan.RequestID, Resource: assignment.ID, Payload: payload, VPPCtlCommands: gatewayNATEgressCommands(payload, false)})
	}
	return operations
}

func gatewayNATPrefix(behavior nat.Behavior) string {
	if behavior == nat.BehaviorFullCone {
		return "nat44 ei"
	}
	return "nat44"
}

func gatewayNATEgressCommands(payload GatewayNATEgress, deleting bool) []string {
	prefix := gatewayNATPrefix(payload.Behavior)
	commands := []string{}
	suffix := ""
	if deleting {
		suffix = " del"
	} else {
		commands = append(commands, natInitializeCommands(payload.Behavior)...)
	}
	// Inside roles are shared by all NAT WANs; retiring one WAN must not
	// remove an inside role still used by another WAN.
	if !deleting {
		for _, name := range payload.Inside {
			commands = append(commands, fmt.Sprintf("?set interface %s in %s", prefix, name))
		}
	} else {
		for _, name := range payload.Inside {
			if !slices.Contains(payload.RetainInside, name) {
				commands = append(commands, fmt.Sprintf("?set interface %s in %s del", prefix, name))
			}
		}
	}
	commands = append(commands,
		fmt.Sprintf("?set interface %s out %s%s", prefix, payload.Outside, suffix),
		fmt.Sprintf("?%s add interface address %s%s", prefix, payload.Outside, suffix),
		"show "+prefix+" interfaces", "show "+prefix+" interface address")
	return commands
}

func verifyGatewayNATEgress(payload GatewayNATEgress, results []VPPCTLCommandResult) error {
	prefix := gatewayNATPrefix(payload.Behavior)
	interfaces, err := commandOutputLast(results, "show "+prefix+" interfaces")
	if err != nil {
		return err
	}
	for _, name := range payload.Inside {
		if !gatewayNATInterfaceRolePresent(interfaces, name, "in") {
			return snapshotDecodeError("NAT inside role is absent for %s", name)
		}
	}
	if !gatewayNATInterfaceRolePresent(interfaces, payload.Outside, "out") {
		return snapshotDecodeError("NAT outside role is absent for %s", payload.Outside)
	}
	tracked, err := commandOutputLast(results, "show "+prefix+" interface address")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(tracked, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == payload.Outside {
			return nil
		}
	}
	return snapshotDecodeError("NAT address tracking is absent for %s", payload.Outside)
}

func gatewayNATInterfaceRolePresent(output, name, role string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != name {
			continue
		}
		for _, field := range fields[1:] {
			if field == role {
				return true
			}
		}
	}
	return false
}
