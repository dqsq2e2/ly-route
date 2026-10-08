package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
)

func TestInterfaceRolePatchCreatesOnlyDiscoveredInterfaceAndKeepsStableIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:interface-role-discovery?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{
			{"id": "enp0s20f2", "name": "enp0s20f2", "port_label": "GE1", "work_mode": "kernel_stack"},
			{"id": "enp0s20f3", "name": "enp0s20f3", "port_label": "GE2", "work_mode": "kernel_stack", "rx_bps": 123},
		}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp0s20f2")
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]

	for _, test := range []struct {
		id   string
		want int
	}{
		{"missing-interface", http.StatusNotFound},
		{"GE1", http.StatusUnprocessableEntity},
	} {
		response := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/interfaces/"+test.id, `{"gateway_role":"wan"}`, cookie)
		if response.Code != test.want {
			t.Fatalf("PATCH %s = %d %s; want %d", test.id, response.Code, response.Body.String(), test.want)
		}
	}
	documents, _ := store.Configs(ctx, "interface")
	if len(documents) != 0 {
		t.Fatalf("rejected patches wrote %d interface records", len(documents))
	}

	for _, patch := range []struct {
		id, payload string
	}{
		{"GE2", `{"gateway_role":"lan","role_configured":true,"cidr":"192.0.2.1/24"}`},
		{"enp0s20f3", `{"gateway_role":"wan","role_configured":true}`},
		{"GE2", `{"gateway_role":"lan","role_configured":true}`},
	} {
		response := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/interfaces/"+patch.id, patch.payload, cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d %s", patch.id, response.Code, response.Body.String())
		}
	}
	documents, err = store.Configs(ctx, "interface")
	if err != nil || len(documents) != 1 || documents[0].ResourceID != "enp0s20f3" {
		t.Fatalf("stable interface records = %#v, %v", documents, err)
	}
	var saved map[string]any
	if err := json.Unmarshal(documents[0].Payload, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["cidr"] != "192.0.2.1/24" || saved["gateway_role"] != "lan" {
		t.Fatalf("role patch lost configuration: %#v", saved)
	}
	for _, field := range []string{"rx_bps", "link_state", "stats", "addresses"} {
		if _, exists := saved[field]; exists {
			t.Fatalf("runtime %s persisted as desired configuration", field)
		}
	}
	response := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/interfaces/GE2", "", cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"gateway_role":"lan"`) || !strings.Contains(response.Body.String(), `"system_name":"enp0s20f3"`) {
		t.Fatalf("role readback = %d %s", response.Code, response.Body.String())
	}
}
