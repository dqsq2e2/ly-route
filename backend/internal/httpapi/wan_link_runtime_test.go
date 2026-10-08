package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
)

type fakeWANLinkRuntime struct {
	current map[string]WANLinkRuntimeObservation
	err     error
}

func (observer fakeWANLinkRuntime) ObserveWANLinks(context.Context, []WANLinkRuntimeRequest) (map[string]WANLinkRuntimeObservation, error) {
	return observer.current, observer.err
}

func TestWANLinkRuntimeDecoratesWithoutMutatingDesiredState(t *testing.T) {
	oldInventory := hostInterfaceInventory
	t.Cleanup(func() { hostInterfaceInventory = oldInventory })
	link := "up"
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{{"id": "enp4s0f0", "link_state": link}}
	}
	observer := fakeWANLinkRuntime{current: map[string]WANLinkRuntimeObservation{
		"enp4s0f0": {Address: "192.168.1.221/24", Gateway: "192.168.1.1", AdminUp: true, RouteReady: true, DHCPState: "DHCP_BOUND"},
	}}
	server := New(WithWANLinkRuntime(observer), WithClock(fixedClock()))
	items := []map[string]any{{"id": "wan1", "type": "dhcp4", "interface_id": "enp4s0f0", "runtime_state": "desired_not_applied", "current_address": "stale-address"}}
	for _, test := range []struct {
		name, physical, state string
		disabled, missing     bool
		err                   error
	}{
		{"bound", "up", "up", false, false, nil},
		{"cable down", "down", "down", false, false, nil},
		{"disabled", "up", "down", true, false, nil},
		{"unknown carrier", "", "unavailable", false, false, nil},
		{"VPP read failed", "up", "unavailable", false, false, errors.New("socket unavailable")},
		{"no observation", "up", "unavailable", false, true, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			link = test.physical
			item := cloneObject(items[0])
			item["enabled"] = !test.disabled
			current := observer
			current.err = test.err
			if test.missing {
				current.current = nil
			}
			server.wanLinkRuntime = current
			got := server.decorateWANLinkRuntimeStates(context.Background(), []map[string]any{item})[0]
			if got["operational_state"] != test.state {
				t.Fatalf("WAN state = %#v", got)
			}
			if got["runtime_state"] != "desired_not_applied" || item["current_address"] != "stale-address" {
				t.Fatalf("desired configuration was altered: %#v, %#v", got, item)
			}
			if (test.err != nil || test.missing) && got["current_address"] != nil {
				t.Fatalf("failed observation retained stale address: %#v", got)
			}
		})
	}
}

func TestWANLinkRuntimeIsExposedOnCollectionAndItem(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:wan-runtime-http-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	oldInventory := hostInterfaceInventory
	t.Cleanup(func() { hostInterfaceInventory = oldInventory })
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{{"id": "wan0", "link_state": "up"}}
	}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithWANLinkRuntime(fakeWANLinkRuntime{current: map[string]WANLinkRuntimeObservation{
			"wan0": {Address: "192.0.2.10/24", Gateway: "192.0.2.1", AdminUp: true, RouteReady: true},
		}}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-runtime","interface_id":"wan0","type":"dhcp4","runtime_state":"desired_not_applied"}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	for _, path := range []string{"/api/v1/gateway/wan-links", "/api/v1/gateway/wan-links/wan-runtime"} {
		response := authenticatedJSONRequest(t, server, http.MethodGet, path, "", cookie)
		for _, required := range []string{`"operational_state":"up"`, `"current_address":"192.0.2.10/24"`, `"route_ready":true`, `"runtime_state":"desired_not_applied"`} {
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), required) {
				t.Fatalf("%s = %d %s, missing %s", path, response.Code, response.Body.String(), required)
			}
		}
	}
	persisted, _, err := server.desiredItem(ctx, "wan_link", "wan-runtime")
	if err != nil || persisted["current_address"] != nil || persisted["operational_state"] != nil {
		t.Fatalf("live observation was persisted: %#v %v", persisted, err)
	}
}
