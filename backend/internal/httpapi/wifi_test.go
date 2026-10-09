package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
)

func wifiTestServer(t *testing.T) (*Server, *persistence.Store, *http.Cookie) {
	t.Helper()
	store, err := persistence.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	server.wifi.run = func(_ context.Context, action string, input any) (json.RawMessage, error) {
		if action == "apply" {
			return json.RawMessage(`{"applied":true}`), nil
		}
		return json.RawMessage(`{"state":"disabled","interface":"wlp1s0","capabilities":{"channels":[]}}`), nil
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatal(login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	return server, store, cookie
}

func wifiTestRequest(t *testing.T, server *Server, cookie *http.Cookie, config wifiConfig) *httptest.ResponseRecorder {
	t.Helper()
	content, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return authenticatedJSONRequest(t, server, http.MethodPut, "/api/v1/wifi", string(content), cookie)
}

func TestWiFiCredentialsAreEncryptedAndNeverReturned(t *testing.T) {
	server, store, cookie := wifiTestServer(t)
	config := defaultWiFiConfig()
	config.SSID, config.Password = "test-radio", "private-wifi-password"
	response := wifiTestRequest(t, server, cookie, config)
	if response.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), config.Password) {
		t.Fatal("password leaked in HTTP response")
	}
	document, err := store.Config(context.Background(), "wifi", "radio0")
	if err != nil || strings.Contains(string(document.Payload), config.Password) {
		t.Fatalf("plaintext configuration: %s, %v", document.Payload, err)
	}
	password, err := store.Secret(context.Background(), "wifi", "radio0", "password")
	if err != nil || password != config.Password {
		t.Fatalf("secret roundtrip failed: %v", err)
	}
	read, _, _, err := server.storedWiFi(context.Background())
	if err != nil || !read.PasswordSet || read.Revision == "" || read.Password != "" {
		t.Fatalf("stored config = %+v, %v", read, err)
	}
	read.SSID = "second-name"
	if response := wifiTestRequest(t, server, cookie, read); response.Code != http.StatusOK {
		t.Fatalf("blank password must retain existing secret: %s", response.Body.String())
	}
	password, _ = store.Secret(context.Background(), "wifi", "radio0", "password")
	if password != config.Password {
		t.Fatal("blank update lost secret")
	}
	read, _, _, _ = server.storedWiFi(context.Background())
	previousRevision := read.Revision
	read.Password = "changed-password-only"
	if response := wifiTestRequest(t, server, cookie, read); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	read, _, _, _ = server.storedWiFi(context.Background())
	if read.Revision == previousRevision {
		t.Fatal("password-only save must invalidate stale revisions without exposing a password hash")
	}
}

func TestWiFiApplyFailureRollsBackConfigAndSecret(t *testing.T) {
	server, store, cookie := wifiTestServer(t)
	config := defaultWiFiConfig()
	config.Password = "original-password"
	if result := wifiTestRequest(t, server, cookie, config); result.Code != http.StatusOK {
		t.Fatal(result.Body.String())
	}
	before, _ := store.Config(context.Background(), "wifi", "radio0")
	config, _, _, _ = server.storedWiFi(context.Background())
	config.SSID, config.Password = "bad-new-name", "replacement-password"
	calls := 0
	server.wifi.run = func(_ context.Context, action string, input any) (json.RawMessage, error) {
		if action == "apply" {
			calls++
			if calls == 1 {
				return nil, errors.New("activation failure")
			}
			if input.(wifiConfig).Password != "original-password" {
				t.Fatal("runtime rollback lost original password")
			}
		}
		return json.RawMessage(`{}`), nil
	}
	if response := wifiTestRequest(t, server, cookie, config); response.Code != http.StatusBadGateway {
		t.Fatalf("failed apply = %d", response.Code)
	}
	after, _ := store.Config(context.Background(), "wifi", "radio0")
	if string(after.Payload) != string(before.Payload) || calls != 2 {
		t.Fatalf("rollback did not restore config/runtime: %d", calls)
	}
	password, _ := store.Secret(context.Background(), "wifi", "radio0", "password")
	if password != "original-password" {
		t.Fatal("rollback did not restore encrypted credential")
	}
}

func TestWiFiInitialApplyFailureRemovesNewRecord(t *testing.T) {
	server, store, cookie := wifiTestServer(t)
	calls := 0
	server.wifi.run = func(_ context.Context, action string, _ any) (json.RawMessage, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("activation failure")
		}
		return json.RawMessage(`{}`), nil
	}
	config := defaultWiFiConfig()
	config.Password = "temporary-password"
	if result := wifiTestRequest(t, server, cookie, config); result.Code != http.StatusBadGateway {
		t.Fatal(result.Body.String())
	}
	if _, err := store.Config(context.Background(), "wifi", "radio0"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("new record survived failed apply: %v", err)
	}
	if _, err := store.Secret(context.Background(), "wifi", "radio0", "password"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("new secret survived failed apply: %v", err)
	}
}

func TestWiFiRejectsStaleRevision(t *testing.T) {
	server, _, cookie := wifiTestServer(t)
	config := defaultWiFiConfig()
	if result := wifiTestRequest(t, server, cookie, config); result.Code != http.StatusOK {
		t.Fatal(result.Body.String())
	}
	if result := wifiTestRequest(t, server, cookie, config); result.Code != http.StatusConflict {
		t.Fatalf("stale revision = %d", result.Code)
	}
}

func TestWiFiValidationAndAuthentication(t *testing.T) {
	server, _, cookie := wifiTestServer(t)
	response := request(t, server, http.MethodGet, "/api/v1/wifi")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request = %d", response.Code)
	}
	for _, change := range []func(*wifiConfig){
		func(c *wifiConfig) { c.Enabled = true },
		func(c *wifiConfig) { c.SSID = "bad\nssid" },
		func(c *wifiConfig) { c.Security = "open" },
		func(c *wifiConfig) { c.Width = 80 },
		func(c *wifiConfig) { c.Password = "short" },
		func(c *wifiConfig) { c.Country = "CN\ninterface=eth0" },
	} {
		config := defaultWiFiConfig()
		change(&config)
		if response := wifiTestRequest(t, server, cookie, config); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid config accepted: %+v -> %d", config, response.Code)
		}
	}
}

func TestWiFiCustomNetworkPersistsAndLegacyUpdatePreservesIt(t *testing.T) {
	server, store, cookie := wifiTestServer(t)
	config := defaultWiFiConfig()
	config.APCIDR, config.DHCPPoolStart, config.DHCPPoolEnd = "10.42.7.1/24", "10.42.7.20", "10.42.7.90"
	if response := wifiTestRequest(t, server, cookie, config); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	config, _, _, _ = server.storedWiFi(context.Background())
	config.APCIDR, config.DHCPPoolStart, config.DHCPPoolEnd = "", "", ""
	config.SSID = "legacy-client-save"
	var applied wifiConfig
	server.wifi.run = func(_ context.Context, action string, input any) (json.RawMessage, error) {
		if action == "apply" {
			applied = input.(wifiConfig)
		}
		return json.RawMessage(`{}`), nil
	}
	if response := wifiTestRequest(t, server, cookie, config); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	if applied.APCIDR != "10.42.7.1/24" || applied.DHCPPoolStart != "10.42.7.20" || applied.DHCPPoolEnd != "10.42.7.90" {
		t.Fatalf("legacy update reset custom network: %+v", applied)
	}
	document, _ := store.Config(context.Background(), "wifi", "radio0")
	if !strings.Contains(string(document.Payload), `"ap_cidr":"10.42.7.1/24"`) {
		t.Fatal("custom subnet was not persisted")
	}
}

func TestWiFiRejectsInvalidPoolWithoutApplyingOrPersisting(t *testing.T) {
	for _, change := range []func(*wifiConfig){
		func(c *wifiConfig) { c.APCIDR = "192.168.89.0/24" },
		func(c *wifiConfig) { c.APCIDR = "::1/64" },
		func(c *wifiConfig) { c.DHCPPoolStart = "192.168.89.201" },
		func(c *wifiConfig) { c.DHCPPoolEnd = "192.168.90.200" },
		func(c *wifiConfig) { c.DHCPPoolStart = "192.168.89.1" },
		func(c *wifiConfig) { c.DHCPPoolEnd = "192.168.89.255" },
	} {
		server, store, cookie := wifiTestServer(t)
		config := defaultWiFiConfig()
		change(&config)
		server.wifi.run = func(context.Context, string, any) (json.RawMessage, error) {
			t.Fatal("invalid network reached runtime")
			return nil, nil
		}
		if response := wifiTestRequest(t, server, cookie, config); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid pool accepted: %+v", config)
		}
		if _, err := store.Config(context.Background(), "wifi", "radio0"); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("invalid network persisted: %v", err)
		}
	}
}

func TestWiFiScanFailureDoesNotApplyOrClaimRollback(t *testing.T) {
	server, _, cookie := wifiTestServer(t)
	server.wifi.run = func(_ context.Context, action string, _ any) (json.RawMessage, error) {
		if action != "scan" {
			t.Fatalf("scan invoked %s", action)
		}
		return nil, wifiServiceError(action, []byte(`{"error":{"code":"wifi_scan_aborted"}}`))
	}
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/wifi/scan", `{}`, cookie)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "restored") ||
		!strings.Contains(response.Body.String(), "当前配置未更改") {
		t.Fatalf("misleading scan error: %s", response.Body.String())
	}
	err := wifiServiceError("scan", []byte(`{"error":{"code":"secret-password","message":"private credential"}}`))
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatal("helper error leaked untrusted details")
	}
}
