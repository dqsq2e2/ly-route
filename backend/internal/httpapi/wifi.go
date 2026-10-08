package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
)

type wifiConfig struct {
	Enabled     bool   `json:"enabled"`
	Mode        string `json:"mode"`
	SSID        string `json:"ssid"`
	Country     string `json:"country"`
	Band        string `json:"band"`
	Channel     int    `json:"channel"`
	Width       int    `json:"width"`
	Security    string `json:"security"`
	Hidden      bool   `json:"hidden"`
	Isolate     bool   `json:"isolate"`
	MaxClients  int    `json:"max_clients"`
	Password    string `json:"password,omitempty"`
	PasswordSet bool   `json:"password_set"`
	Revision    string `json:"revision,omitempty"`
}

type wifiAPI struct {
	mu  sync.Mutex
	run func(context.Context, string, any) (json.RawMessage, error)
}

func defaultWiFiConfig() wifiConfig {
	return wifiConfig{Mode: "ap", SSID: "LyRoute", Band: "2g", Channel: 1,
		Width: 20, Security: "wpa2", Isolate: true, MaxClients: 32}
}

func newWiFiAPI() *wifiAPI {
	return &wifiAPI{run: func(ctx context.Context, action string, input any) (json.RawMessage, error) {
		ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "/usr/lib/ly-route/wifi-runtime.py", action)
		if input != nil {
			content, err := json.Marshal(input)
			if err != nil {
				return nil, err
			}
			command.Stdin = bytes.NewReader(content)
		}
		output, err := command.Output()
		if err != nil {
			// Never return process arguments, stderr or credentials to the client.
			return nil, fmt.Errorf("wireless service %s failed; previous configuration restored", action)
		}
		if len(output) > 1<<20 || !json.Valid(output) {
			return nil, errors.New("wireless service returned invalid status")
		}
		return output, nil
	}}
}

func (config wifiConfig) validate(password string) error {
	if config.Mode != "ap" && config.Mode != "client" {
		return errors.New("mode must be ap or client")
	}
	if !utf8.ValidString(config.SSID) || len(config.SSID) < 1 || len(config.SSID) > 32 ||
		strings.ContainsAny(config.SSID, "\x00\r\n") {
		return errors.New("SSID must contain 1 to 32 UTF-8 bytes without control characters")
	}
	if config.Country != "" && !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(config.Country) {
		return errors.New("country must be a two-letter regulatory code")
	}
	if config.Enabled && config.Country == "" {
		return errors.New("select the device's actual country before enabling WiFi")
	}
	if config.Band != "2g" && config.Band != "5g" {
		return errors.New("band must be 2g or 5g")
	}
	if config.Width != 20 && config.Width != 40 && config.Width != 80 ||
		config.Band == "2g" && config.Width == 80 {
		return errors.New("unsupported channel width")
	}
	if config.Channel < 1 || config.Channel > 196 || config.MaxClients < 1 || config.MaxClients > 128 {
		return errors.New("invalid channel or client limit")
	}
	if config.Security != "wpa2" && config.Security != "wpa3" && config.Security != "mixed" {
		return errors.New("security must be wpa2, wpa3 or mixed; open and WEP networks are not allowed")
	}
	if password != "" && (len(password) < 8 || len(password) > 63 || !utf8.ValidString(password) ||
		strings.ContainsAny(password, "\x00\r\n")) {
		return errors.New("WiFi password must contain 8 to 63 bytes without control characters")
	}
	if config.Enabled && password == "" {
		return errors.New("a WiFi password is required")
	}
	return nil
}

func (server *Server) storedWiFi(ctx context.Context) (wifiConfig, string, *persistence.ConfigDocument, error) {
	config := defaultWiFiConfig()
	if server.store == nil {
		return config, "", nil, errors.New("wireless configuration store is unavailable")
	}
	document, err := server.store.Config(ctx, "wifi", "radio0")
	if errors.Is(err, persistence.ErrNotFound) {
		return config, "", nil, nil
	}
	if err != nil {
		return config, "", nil, err
	}
	if err := json.Unmarshal(document.Payload, &config); err != nil {
		return config, "", nil, err
	}
	password, err := server.store.Secret(ctx, "wifi", "radio0", "password")
	if err != nil && !errors.Is(err, persistence.ErrNotFound) {
		return config, "", nil, err
	}
	config.Password = ""
	config.PasswordSet = password != ""
	config.Revision = document.PayloadHash
	return config, password, &document, nil
}

func (server *Server) handleWiFi(w http.ResponseWriter, r *http.Request) {
	session, authenticated := server.sessionFromRequest(r)
	if !authenticated {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	scan := strings.HasSuffix(r.URL.Path, "/scan")
	if !server.profile.AllowsConfigResource("wifi") {
		writeError(w, r, http.StatusNotFound, "not_found", "wireless is unavailable for this product")
		return
	}
	if (!scan && r.Method != http.MethodGet && r.Method != http.MethodPut) ||
		(scan && r.Method != http.MethodPost) {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if r.Method != http.MethodGet {
		if session.Role != "admin" {
			writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
		if server.passwordChangeRequired(w, r, session, "wifi", "update") {
			return
		}
	}
	api := server.wifi
	api.mu.Lock()
	defer api.mu.Unlock()
	if scan {
		output, err := api.run(r.Context(), "scan", nil)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "wifi_scan_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, output)
		return
	}
	config, password, previous, err := server.storedWiFi(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "wifi_store_failed", "wireless configuration could not be read")
		return
	}
	if r.Method == http.MethodPut {
		var desired wifiConfig
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&desired); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_json", "invalid wireless configuration")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			writeError(w, r, http.StatusBadRequest, "invalid_json", "expected one JSON object")
			return
		}
		if desired.Revision != config.Revision {
			writeError(w, r, http.StatusConflict, "wifi_conflict", "wireless configuration changed; reload before saving")
			return
		}
		newPassword := desired.Password
		if newPassword == "" {
			newPassword = password
		}
		if err := desired.validate(newPassword); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_wifi_config", err.Error())
			return
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			writeError(w, r, http.StatusInternalServerError, "wifi_revision_failed", "wireless revision could not be generated")
			return
		}
		desired.Password, desired.Revision = "", hex.EncodeToString(nonce)
		desired.PasswordSet = newPassword != ""
		content, _ := json.Marshal(desired)
		digest := sha256.Sum256(content)
		document := persistence.ConfigDocument{ResourceType: "wifi", ResourceID: "radio0", Payload: content,
			PayloadHash: hex.EncodeToString(digest[:]), UpdatedAt: server.now()}
		if err := server.store.SaveConfigWithSecrets(r.Context(), document, map[string]string{"password": newPassword}); err != nil {
			writeError(w, r, http.StatusInternalServerError, "wifi_store_failed", "wireless configuration could not be persisted")
			return
		}
		runtimeConfig := desired
		runtimeConfig.Password = newPassword
		if _, err := api.run(r.Context(), "apply", runtimeConfig); err != nil {
			// Rollback must survive cancellation of the original HTTP request.
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			var rollbackErr error
			if previous == nil {
				rollbackErr = server.store.DeleteConfig(ctx, "wifi", "radio0")
			} else {
				rollbackErr = server.store.SaveConfigWithSecrets(ctx, *previous, map[string]string{"password": password})
			}
			config.Password = password
			_, runtimeErr := api.run(ctx, "apply", config)
			message := "wireless service rejected the configuration; previous configuration restored"
			if rollbackErr != nil || runtimeErr != nil {
				message = "wireless apply and rollback failed; check service state before retrying"
			}
			server.recordAudit(session.Username, session.Role, "wifi", "update", "failed", message, r)
			writeError(w, r, http.StatusBadGateway, "wifi_apply_failed", message)
			return
		}
		desired.Revision = document.PayloadHash
		config = desired
		server.recordAudit(session.Username, session.Role, "wifi", "update", "success", "", r)
	}
	status, err := api.run(r.Context(), "status", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "config": config, "error": "wireless runtime is unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "config": config, "status": status})
}

func (server *Server) restoreWiFi() {
	if server.profile.ID() != product.Gateway().ID() || server.store == nil {
		return
	}
	if _, err := os.Stat("/usr/lib/ly-route/wifi-runtime.py"); err != nil {
		return
	}
	config, password, previous, err := server.storedWiFi(context.Background())
	if err != nil || previous == nil {
		return
	}
	config.Password = password
	if config.validate(password) == nil {
		_, _ = server.wifi.run(context.Background(), "apply", config)
	}
}
