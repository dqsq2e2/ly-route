package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
)

func TestManagementGatewayCanBeClearedAndOmittedPatchPreservesIt(t *testing.T) {
	store, err := persistence.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("LY_ROUTE_LAN_INTERFACE", "enp0s20f2")
	t.Setenv("LY_ROUTE_LAN_CIDR", "192.168.88.254/24")
	t.Setenv("LY_ROUTE_MANAGEMENT_GATEWAY", "192.168.88.1")
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatal(login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	for _, step := range []struct {
		payload string
		want    string
	}{
		{`{"confirm_change":true}`, "192.168.88.1"},
		{`{"gateway":"","confirm_change":true}`, ""},
		{`{"confirm_change":true}`, ""},
		{`{"gateway":"192.168.88.2","confirm_change":true}`, "192.168.88.2"},
		{`{"gateway":"   ","confirm_change":true}`, ""},
	} {
		response := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/management/network", step.payload, cookie)
		if response.Code != http.StatusOK {
			t.Fatal(response.Body.String())
		}
		reloaded := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/management/network", "", cookie)
		if !strings.Contains(reloaded.Body.String(), `"gateway":"`+step.want+`"`) {
			t.Fatalf("payload=%s did not preserve/clear gateway: %s", step.payload, reloaded.Body.String())
		}
	}
	invalid := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/management/network",
		`{"gateway":"192.168.1.1","confirm_change":true}`, cookie)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("off-subnet gateway accepted: %s", invalid.Body.String())
	}
	reloaded := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/management/network", "", cookie)
	if !strings.Contains(reloaded.Body.String(), `"gateway":""`) {
		t.Fatalf("invalid update changed stored gateway: %s", reloaded.Body.String())
	}
}
