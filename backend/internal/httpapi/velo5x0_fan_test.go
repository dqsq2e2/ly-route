package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

var fanTestNow = time.Unix(1791400000, 0)

func newFanTestServer(t *testing.T) (*Server, string, *http.Cookie) {
	t.Helper()
	root := t.TempDir()
	writeFanTestFile(t, root, "etc/ly-route/hardware", []byte("velo5x0\n"), 0o644)
	writeFanTestFile(t, root, "sys/class/dmi/id/board_name", []byte("EDGE520\n"), 0o444)
	server := New(WithVelo5x0FanRoot(root), WithClock(func() time.Time { return fanTestNow }))
	session := server.sessions.create("admin", "admin", false)
	return server, root, sessionCookie(session.ID, false)
}

func writeFanTestFile(t *testing.T, root, relative string, content []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func fanTestResponse(t *testing.T, res *httptest.ResponseRecorder) Velo5x0FanResponse {
	t.Helper()
	if res.Code != http.StatusOK {
		t.Fatalf("fan response = %d: %s", res.Code, res.Body.String())
	}
	var response Velo5x0FanResponse
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func fanTestFloat(value float64) *float64 { return &value }

func fanTestStatus() Velo5x0FanStatus {
	return Velo5x0FanStatus{
		Running: true, OutputPWM: fanTestFloat(16),
		Temperatures: Velo5x0FanTemperatures{CPU: fanTestFloat(45.5), WiFi: fanTestFloat(39), Board: fanTestFloat(42.25)},
		Sensors: []Velo5x0FanSensor{
			{Source: "cpu", Sensor: "temp2_input", Label: "Core 0", Value: 45.5},
			{Source: "wifi", Sensor: "temp1_input", Label: "WiFi", Value: 39},
			{Source: "board", Sensor: "temp3_input", Label: "Board 3", Value: 42.25},
		},
		EffectiveTemperature: fanTestFloat(45.5), Mode: "curve",
		ConfigRevision: "default", UpdatedAt: fanTestNow.Unix(),
	}
}

func writeFanTestStatus(t *testing.T, root string, status Velo5x0FanStatus) {
	t.Helper()
	content, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	writeFanTestFile(t, root, velo5x0FanStatusPath, content, 0o600)
}

func TestFanAvailabilityGate(t *testing.T) {
	tests := []struct {
		name, hardware, board string
		available             bool
	}{
		{"generic", "generic", "EDGE520", false},
		{"missing hardware", "", "EDGE520", false},
		{"profile without board", "velo5x0", "", false},
		{"wrong board", "velo5x0", "EDGE510", false},
		{"wrong board case", "velo5x0", "edge520", false},
		{"wrong hardware case", "VELO5X0", "EDGE520", false},
		{"EDGE520", "velo5x0", "EDGE520", true},
		{"EDGE540", "velo5x0", "EDGE540", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, root, cookie := newFanTestServer(t)
			hardwarePath := filepath.Join(root, "etc/ly-route/hardware")
			if test.hardware == "" {
				if err := os.Remove(hardwarePath); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFanTestFile(t, root, "etc/ly-route/hardware", []byte(test.hardware+"\n"), 0o644)
			}
			boardPath := filepath.Join(root, "sys/class/dmi/id/board_name")
			if err := os.Remove(boardPath); err != nil {
				t.Fatal(err)
			}
			if test.board != "" {
				writeFanTestFile(t, root, "sys/class/dmi/id/board_name", []byte(test.board+"\n"), 0o444)
			}
			response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
			if response.Available != test.available || response.Status.Running || response.Status.OutputPWM != nil {
				t.Fatalf("fan availability response = %#v", response)
			}
			if !test.available {
				res := authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", `{}`, cookie)
				if res.Code != http.StatusNotFound || !strings.Contains(res.Body.String(), `"code":"fan_unavailable"`) {
					t.Fatalf("unsupported fan PUT = %d: %s", res.Code, res.Body.String())
				}
			}
			if _, err := os.Stat(filepath.Join(root, velo5x0FanConfigPath)); !os.IsNotExist(err) {
				t.Fatalf("availability checks created config: %v", err)
			}
		})
	}
}

func TestFanCanonicalDMIPath(t *testing.T) {
	server, root, cookie := newFanTestServer(t)
	classPath := filepath.Join(root, "sys/class/dmi/id")
	if err := os.Remove(filepath.Join(classPath, "board_name")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(classPath); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(root, "sys/devices/virtual/dmi/id")
	writeFanTestFile(t, root, "sys/devices/virtual/dmi/id/board_name", []byte("EDGE540\n"), 0o444)
	if err := os.Symlink(canonical, classPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
	if !response.Available {
		t.Fatal("canonical DMI path should work with the normal sysfs class alias")
	}
}

func TestFanDefaultContract(t *testing.T) {
	server, root, cookie := newFanTestServer(t)
	res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie)
	response := fanTestResponse(t, res)
	want := Velo5x0FanConfig{
		Mode: "curve", ManualPWM: 31, TempSource: "cpu", CPUStatistic: "max",
		CPUSensor: "temp2_input", CurveProfile: "linear", MinPWM: 16,
		StopTemperature: 42, StartTemperature: 45, FullTemperature: 60, PollInterval: 3,
		Curve: []Velo5x0FanCurvePoint{{45, 16}, {50, 43}, {55, 71}, {60, 100}},
	}
	if !response.Available || !reflect.DeepEqual(response.Config, want) {
		t.Fatalf("default config = %#v", response.Config)
	}
	if response.Status.Running || response.Status.UpdatedAt != 0 || response.Status.ConfigRevision != "default" || response.Status.Error == "" {
		t.Fatalf("default status = %#v", response.Status)
	}
	for _, fragment := range []string{`"output_pwm":null`, `"cpu":null`, `"wifi":null`, `"board":null`, `"effective_temperature":null`, `"sensors":[]`} {
		if !strings.Contains(res.Body.String(), fragment) {
			t.Errorf("default contract missing %s: %s", fragment, res.Body.String())
		}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil || len(envelope) != 3 {
		t.Fatalf("response envelope = %#v, error = %v", envelope, err)
	}
	if _, err := os.Stat(filepath.Join(root, velo5x0FanConfigPath)); !os.IsNotExist(err) {
		t.Fatalf("GET must not persist defaults: %v", err)
	}
}

func TestFanAuthenticationAndPermissions(t *testing.T) {
	server, root, admin := newFanTestServer(t)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		res := requestBody(t, server, method, "/api/system/fan", `{}`)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s = %d: %s", method, res.Code, res.Body.String())
		}
	}
	readonly := server.sessions.create("readonly", "readonly", false)
	readonlyCookie := sessionCookie(readonly.ID, false)
	if res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", readonlyCookie); res.Code != http.StatusOK {
		t.Fatalf("readonly GET = %d: %s", res.Code, res.Body.String())
	}
	if res := authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", `{}`, readonlyCookie); res.Code != http.StatusForbidden {
		t.Fatalf("readonly PUT = %d: %s", res.Code, res.Body.String())
	}
	locked := server.sessions.create("admin", "admin", true)
	if res := authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", `{}`, sessionCookie(locked.ID, false)); res.Code != http.StatusForbidden || !strings.Contains(res.Body.String(), "password_change_required") {
		t.Fatalf("password-change PUT = %d: %s", res.Code, res.Body.String())
	}
	bearerRequest := httptest.NewRequest(http.MethodGet, "/api/system/fan", nil)
	bearerRequest.Header.Set("Authorization", "Bearer "+readonly.ID)
	bearerResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(bearerResponse, bearerRequest)
	if bearerResponse.Code != http.StatusOK {
		t.Fatalf("bearer GET = %d: %s", bearerResponse.Code, bearerResponse.Body.String())
	}
	res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/system/fan", `{}`, admin)
	if res.Code != http.StatusMethodNotAllowed || res.Header().Get("Allow") != "GET, PUT" {
		t.Fatalf("POST = %d, Allow = %q", res.Code, res.Header().Get("Allow"))
	}
	if _, err := os.Stat(filepath.Join(root, velo5x0FanConfigPath)); !os.IsNotExist(err) {
		t.Fatalf("denied requests changed config: %v", err)
	}
	events, err := server.auditEvents(context.Background())
	if err != nil || len(events) != 2 || events[0].Status != "denied" || events[1].Status != "denied" {
		t.Fatalf("denied audits = %#v, error = %v", events, err)
	}
}

func TestFanValidationPreservesOldConfig(t *testing.T) {
	server, root, cookie := newFanTestServer(t)
	fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", `{"mode":"manual","manual_pwm":31}`, cookie))
	path := filepath.Join(root, velo5x0FanConfigPath)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, body string }{
		{"unknown mode", `{"mode":"auto"}`},
		{"empty mode", `{"mode":""}`},
		{"manual too low", `{"manual_pwm":-1}`},
		{"manual too high", `{"manual_pwm":101}`},
		{"manual fractional", `{"manual_pwm":31.5}`},
		{"manual string", `{"manual_pwm":"31"}`},
		{"unknown source", `{"temp_source":"cpu_wifi_max"}`},
		{"unknown statistic", `{"cpu_statistic":"avg"}`},
		{"sensor path", `{"cpu_sensor":"../../temp2_input"}`},
		{"sensor missing number", `{"cpu_sensor":"temp_input"}`},
		{"sensor suffix", `{"cpu_sensor":"temp2_input/other"}`},
		{"sensor newline", "{\"cpu_sensor\":\"temp2_input\\n\"}"},
		{"unknown profile", `{"curve_profile":"quadratic"}`},
		{"minimum negative", `{"min_pwm":-1}`},
		{"minimum too high", `{"min_pwm":101}`},
		{"minimum fractional", `{"min_pwm":16.5}`},
		{"stop negative", `{"stop_temperature":-1}`},
		{"stop too high", `{"stop_temperature":101}`},
		{"stop equals start", `{"stop_temperature":45}`},
		{"stop above start", `{"stop_temperature":46}`},
		{"start too high", `{"start_temperature":101}`},
		{"start negative", `{"start_temperature":-1}`},
		{"full equals start", `{"full_temperature":45}`},
		{"full below start", `{"full_temperature":44}`},
		{"full too high", `{"full_temperature":101}`},
		{"poll zero", `{"poll_interval":0}`},
		{"poll too high", `{"poll_interval":31}`},
		{"poll fractional", `{"poll_interval":3.5}`},
		{"curve empty", `{"curve":[]}`},
		{"curve three", `{"curve":[{"temperature":45,"pwm":16},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71}]}`},
		{"curve five", `{"curve":[{"temperature":45,"pwm":16},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100},{"temperature":65,"pwm":100}]}`},
		{"curve equal", `{"curve":[{"temperature":45,"pwm":16},{"temperature":45,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"curve descending", `{"curve":[{"temperature":45,"pwm":16},{"temperature":40,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"curve negative", `{"curve":[{"temperature":-1,"pwm":16},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"curve too hot", `{"curve":[{"temperature":45,"pwm":16},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":101,"pwm":100}]}`},
		{"curve pwm negative", `{"curve":[{"temperature":45,"pwm":-1},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"curve pwm too high", `{"curve":[{"temperature":45,"pwm":101},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"curve pwm fractional", `{"curve":[{"temperature":45,"pwm":16.5},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"curve missing pwm", `{"curve":[{"temperature":0},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"curve missing temperature", `{"curve":[{"pwm":0},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"curve unknown key", `{"curve":[{"temperature":45,"pwm":16,"path":"/tmp/a"},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"unknown path", `{"path":"/tmp/arbitrary"}`},
		{"unknown root", `{"root":"/tmp/arbitrary"}`},
		{"wrapped config", `{"config":{}}`},
		{"duplicate", `{"manual_pwm":0,"manual_pwm":100}`},
		{"duplicate curve field", `{"curve":[{"temperature":0,"pwm":0,"pwm":10},{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"null config", `null`},
		{"null field", `{"manual_pwm":null}`},
		{"null curve", `{"curve":null}`},
		{"null point", `{"curve":[null,{"temperature":50,"pwm":43},{"temperature":55,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"not object", `[]`},
		{"empty", ``},
		{"truncated", `{"mode":`},
		{"trailing object", `{} {}`},
		{"trailing garbage", `{} trailing`},
		{"NaN", `{"start_temperature":NaN}`},
		{"infinity", `{"start_temperature":1e999}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res := authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", test.body, cookie)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("invalid fan PUT = %d: %s", res.Code, res.Body.String())
			}
			current, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(original, current) {
				t.Fatalf("invalid request changed old config: %v", err)
			}
		})
	}
	t.Run("bounded body", func(t *testing.T) {
		res := authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", `{}`+strings.Repeat(" ", velo5x0FanConfigLimit), cookie)
		if res.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized PUT = %d: %s", res.Code, res.Body.String())
		}
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(original, current) {
			t.Fatalf("oversized request changed old config: %v", err)
		}
	})
}

func TestFanValidZeroModesAndCustomThresholds(t *testing.T) {
	server, root, cookie := newFanTestServer(t)
	tests := []struct{ name, body string }{
		{"defaults", `{}`},
		{"manual zero", `{"mode":"manual","manual_pwm":0,"min_pwm":0,"stop_temperature":0}`},
		{"manual full", `{"mode":"manual","manual_pwm":100,"min_pwm":100,"poll_interval":30}`},
		{"always run zero start", `{"stop_temperature":0,"start_temperature":0,"full_temperature":0.5,"poll_interval":1}`},
		{"fractional thresholds", `{"stop_temperature":42.1,"start_temperature":45.2,"full_temperature":60.3}`},
		{"custom independent thresholds", `{"curve_profile":"custom","stop_temperature":0,"start_temperature":80,"full_temperature":100,"curve":[{"temperature":0,"pwm":0},{"temperature":20.5,"pwm":43},{"temperature":40.25,"pwm":71},{"temperature":60,"pwm":100}]}`},
		{"custom descending PWM", `{"curve_profile":"custom","curve":[{"temperature":0,"pwm":100},{"temperature":20,"pwm":71},{"temperature":40,"pwm":43},{"temperature":100,"pwm":0}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", test.body, cookie))
			readback := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
			if !reflect.DeepEqual(readback.Config, response.Config) {
				t.Fatalf("GET config = %#v, PUT = %#v", readback.Config, response.Config)
			}
			content, err := os.ReadFile(filepath.Join(root, velo5x0FanConfigPath))
			var persisted Velo5x0FanConfig
			if err != nil || json.Unmarshal(content, &persisted) != nil || !reflect.DeepEqual(persisted, response.Config) {
				t.Fatalf("persisted config = %#v, error = %v", persisted, err)
			}
			if test.name == "manual zero" && (persisted.ManualPWM != 0 || persisted.MinPWM != 0 || persisted.StopTemperature != 0) {
				t.Fatalf("explicit zeroes replaced by defaults: %#v", persisted)
			}
		})
	}
	for _, source := range []string{"cpu", "wifi", "board", "max", "average"} {
		for _, statistic := range []string{"max", "average", "single"} {
			body := `{"temp_source":"` + source + `","cpu_statistic":"` + statistic + `","cpu_sensor":"temp12_input"}`
			fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", body, cookie))
		}
	}
}

func TestFanRejectsNonFiniteConfigValues(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, field := range []string{"stop", "start", "full", "curve"} {
			config := defaultVelo5x0FanConfig()
			switch field {
			case "stop":
				config.StopTemperature = value
			case "start":
				config.StartTemperature = value
			case "full":
				config.FullTemperature = value
			case "curve":
				config.Curve[0].Temperature = value
			}
			if err := config.validate(); err == nil {
				t.Fatalf("non-finite %s = %v accepted", field, value)
			}
		}
	}
}

func TestFanAtomicPersistenceAndDaemonAcknowledgment(t *testing.T) {
	server, root, cookie := newFanTestServer(t)
	oldStatus := fanTestStatus()
	writeFanTestStatus(t, root, oldStatus)
	fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", `{}`, cookie))
	path := filepath.Join(root, velo5x0FanConfigPath)
	oldFile, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer oldFile.Close()
	oldInfo, err := oldFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", `{"mode":"manual","manual_pwm":0}`, cookie))
	if response.Config.Mode != "manual" || response.Config.ManualPWM != 0 || !reflect.DeepEqual(response.Status, oldStatus) {
		t.Fatalf("PUT must preserve unacknowledged daemon status: %#v", response)
	}
	currentInfo, err := os.Stat(path)
	if err != nil || os.SameFile(oldInfo, currentInfo) || currentInfo.Mode().Perm() != 0o600 {
		t.Fatalf("atomic replacement info = %v, error = %v", currentInfo, err)
	}
	oldContent, err := io.ReadAll(oldFile)
	var previous Velo5x0FanConfig
	if err != nil || json.Unmarshal(oldContent, &previous) != nil || previous.Mode != "curve" {
		t.Fatalf("previous open file changed: %s, error = %v", oldContent, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	acknowledged := oldStatus
	acknowledged.Mode = "manual"
	acknowledged.OutputPWM = fanTestFloat(0)
	acknowledged.ConfigRevision = hex.EncodeToString(digest[:])
	writeFanTestStatus(t, root, acknowledged)
	readback := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
	if !reflect.DeepEqual(readback.Config, response.Config) || !reflect.DeepEqual(readback.Status, acknowledged) {
		t.Fatalf("acknowledged GET = %#v", readback)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".velo5x0-fan-") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestFanRuntimeFreshness(t *testing.T) {
	for _, poll := range []int{1, 3, 30} {
		for _, test := range []struct {
			name  string
			age   int64
			fresh bool
		}{
			{"now", 0, true},
			{"boundary", int64(2*poll + 5), true},
			{"stale", int64(2*poll + 6), false},
			{"future", -1, false},
		} {
			t.Run(strconv.Itoa(poll)+"/"+test.name, func(t *testing.T) {
				server, root, cookie := newFanTestServer(t)
				config := defaultVelo5x0FanConfig()
				config.PollInterval = poll
				if err := server.velo5x0Fan.saveConfig(config); err != nil {
					t.Fatal(err)
				}
				status := fanTestStatus()
				status.UpdatedAt -= test.age
				writeFanTestStatus(t, root, status)
				response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
				if response.Status.Running != test.fresh || response.Status.UpdatedAt != status.UpdatedAt {
					t.Fatalf("freshness status = %#v", response.Status)
				}
				if test.fresh {
					if !reflect.DeepEqual(response.Status, status) {
						t.Fatalf("fresh status changed: %#v", response.Status)
					}
				} else if response.Status.OutputPWM != nil || response.Status.Temperatures.CPU != nil || response.Status.Temperatures.WiFi != nil || response.Status.Temperatures.Board != nil || response.Status.EffectiveTemperature != nil || len(response.Status.Sensors) != 0 || !strings.Contains(response.Status.Error, "stale") {
					t.Fatalf("stale status exposes live values: %#v", response.Status)
				}
			})
		}
	}
	t.Run("fresh stopped daemon", func(t *testing.T) {
		server, root, cookie := newFanTestServer(t)
		status := fanTestStatus()
		status.Running = false
		status.OutputPWM = nil
		status.Error = "hardware read failed"
		writeFanTestStatus(t, root, status)
		response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
		if !reflect.DeepEqual(response.Status, status) {
			t.Fatalf("freshness must not invent daemon running state: %#v", response.Status)
		}
	})
	t.Run("zero PWM is not a stopped daemon", func(t *testing.T) {
		server, root, cookie := newFanTestServer(t)
		status := fanTestStatus()
		status.OutputPWM = fanTestFloat(0)
		writeFanTestStatus(t, root, status)
		response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
		if !response.Status.Running || response.Status.OutputPWM == nil || *response.Status.OutputPWM != 0 {
			t.Fatalf("daemon state must not infer actual fan rotation: %#v", response.Status)
		}
	})
}

func TestFanInvalidAndMissingRuntimeStatus(t *testing.T) {
	for _, content := range []string{
		``, `broken`, `{}`, `{"running":true,"updated_at":1e999}`,
		`{"running":true,"mode":"manual","config_revision":"default","updated_at":1791400000000}`,
		`{"running":true,"mode":"manual","config_revision":"default","updated_at":0}`,
		`{"running":true,"mode":"manual","config_revision":"default","updated_at":1791400000,"output_pwm":101}`,
		`{"running":true,"mode":"manual","config_revision":"bad","updated_at":1791400000}`,
		`{"running":true,"mode":"manual","config_revision":"default","updated_at":1791400000,"sensors":[{"source":"other","value":10}]}`,
	} {
		t.Run(content, func(t *testing.T) {
			server, root, cookie := newFanTestServer(t)
			if content != "" {
				writeFanTestFile(t, root, velo5x0FanStatusPath, []byte(content), 0o600)
			}
			response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
			if response.Status.Running || response.Status.OutputPWM != nil || response.Status.Error == "" || response.Status.Sensors == nil {
				t.Fatalf("missing or invalid status = %#v", response.Status)
			}
		})
	}
}

func TestFanDaemonStatusCompatibility(t *testing.T) {
	server, root, cookie := newFanTestServer(t)
	status := fanTestStatus()
	content, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var daemon map[string]any
	if err := json.Unmarshal(content, &daemon); err != nil {
		t.Fatal(err)
	}
	daemon["powered"] = true
	content, err = json.Marshal(daemon)
	if err != nil {
		t.Fatal(err)
	}
	writeFanTestFile(t, root, velo5x0FanStatusPath, content, 0o600)
	res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie)
	response := fanTestResponse(t, res)
	if !reflect.DeepEqual(response.Status, status) || strings.Contains(res.Body.String(), `"powered"`) {
		t.Fatalf("daemon powered field must not change the API contract: %s", res.Body.String())
	}

	status.ConfigRevision = ""
	status.Error = "fan configuration has missing or unknown fields"
	status.OutputPWM = fanTestFloat(100)
	writeFanTestStatus(t, root, status)
	response = fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
	if !reflect.DeepEqual(response.Status, status) {
		t.Fatalf("daemon fail-safe status was discarded: %#v", response.Status)
	}

	writeFanTestFile(t, root, velo5x0FanStatusPath, append(content, bytes.Repeat([]byte(" "), velo5x0FanStatusLimit)...), 0o600)
	response = fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
	if response.Status.Running || response.Status.OutputPWM != nil || response.Status.Error == "" {
		t.Fatalf("oversized runtime file was trusted: %#v", response.Status)
	}
}

func TestFanRefusesUnsafeConfigAndStatusPaths(t *testing.T) {
	t.Run("config symlink", func(t *testing.T) {
		server, root, cookie := newFanTestServer(t)
		target := filepath.Join(t.TempDir(), "old-config.json")
		original := []byte(`{"mode":"manual","manual_pwm":17}`)
		if err := os.WriteFile(target, original, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, velo5x0FanConfigPath)
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			res := authenticatedJSONRequest(t, server, method, "/api/system/fan", `{}`, cookie)
			if res.Code != http.StatusInternalServerError {
				t.Fatalf("symlink %s = %d: %s", method, res.Code, res.Body.String())
			}
		}
		current, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(original, current) {
			t.Fatalf("symlink target changed: %v", err)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink replaced on failed write: %v", err)
		}
	})
	t.Run("status symlink", func(t *testing.T) {
		server, root, cookie := newFanTestServer(t)
		targetRoot := t.TempDir()
		writeFanTestStatus(t, targetRoot, fanTestStatus())
		path := filepath.Join(root, velo5x0FanStatusPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(targetRoot, velo5x0FanStatusPath), path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
		if response.Status.Running || response.Status.OutputPWM != nil || response.Status.Error == "" {
			t.Fatalf("symlink status trusted: %#v", response.Status)
		}
	})
	t.Run("parent symlink", func(t *testing.T) {
		server, root, cookie := newFanTestServer(t)
		targetRoot := t.TempDir()
		writeFanTestFile(t, targetRoot, "ly-route/hardware", []byte("velo5x0"), 0o644)
		targetDir := filepath.Join(targetRoot, "ly-route")
		inside := filepath.Join(root, "etc/ly-route")
		if err := os.Remove(filepath.Join(inside, "hardware")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(inside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(targetDir, inside); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie))
		if response.Available {
			t.Fatal("hardware marker reached through an unsafe parent symlink")
		}
		res := authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", `{}`, cookie)
		if res.Code != http.StatusNotFound {
			t.Fatalf("unsafe parent PUT = %d: %s", res.Code, res.Body.String())
		}
		if _, err := os.Stat(filepath.Join(targetDir, "velo5x0-fan.json")); !os.IsNotExist(err) {
			t.Fatalf("unsafe parent allowed an external write: %v", err)
		}
	})
	t.Run("directory destination", func(t *testing.T) {
		server, root, cookie := newFanTestServer(t)
		path := filepath.Join(root, velo5x0FanConfigPath)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		res := authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", `{}`, cookie)
		if res.Code != http.StatusInternalServerError {
			t.Fatalf("directory destination PUT = %d: %s", res.Code, res.Body.String())
		}
	})
	t.Run("shared writable config", func(t *testing.T) {
		server, root, cookie := newFanTestServer(t)
		path := filepath.Join(root, velo5x0FanConfigPath)
		writeFanTestFile(t, root, velo5x0FanConfigPath, []byte(`{}`), 0o600)
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		res := authenticatedJSONRequest(t, server, http.MethodPut, "/api/system/fan", `{}`, cookie)
		if res.Code != http.StatusInternalServerError {
			t.Fatalf("shared writable config PUT = %d: %s", res.Code, res.Body.String())
		}
	})
}

func TestFanPersistedConfigErrorsDoNotFallBackToDefaults(t *testing.T) {
	for _, content := range []string{`{"mode":"other"}`, `broken`, `{}` + strings.Repeat(" ", velo5x0FanConfigLimit)} {
		t.Run(content[:min(10, len(content))], func(t *testing.T) {
			server, root, cookie := newFanTestServer(t)
			writeFanTestFile(t, root, velo5x0FanConfigPath, []byte(content), 0o600)
			res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", cookie)
			if res.Code != http.StatusInternalServerError || !strings.Contains(res.Body.String(), "fan_config_read_failed") {
				t.Fatalf("invalid persisted config GET = %d: %s", res.Code, res.Body.String())
			}
			current, err := os.ReadFile(filepath.Join(root, velo5x0FanConfigPath))
			if err != nil || string(current) != content {
				t.Fatalf("GET modified invalid persisted config: %v", err)
			}
		})
	}
}

func TestFanRootOptionAndEnvironment(t *testing.T) {
	_, root, _ := newFanTestServer(t)
	t.Setenv("LY_ROUTE_VELO5X0_FAN_ROOT", root)
	server := New()
	session := server.sessions.create("readonly", "readonly", false)
	response := fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", sessionCookie(session.ID, false)))
	if !response.Available {
		t.Fatal("environment fan root was not applied")
	}
	override := t.TempDir()
	server = New(WithVelo5x0FanRoot(override))
	session = server.sessions.create("readonly", "readonly", false)
	response = fanTestResponse(t, authenticatedJSONRequest(t, server, http.MethodGet, "/api/system/fan", "", sessionCookie(session.ID, false)))
	if response.Available {
		t.Fatal("explicit root option must override the environment")
	}
	for _, unsafe := range []string{"relative", root + "/../outside", root + "\x00"} {
		api := newVelo5x0FanAPI(unsafe)
		if api.available() || api.saveConfig(defaultVelo5x0FanConfig()) == nil {
			t.Fatalf("unsafe root accepted: %q", unsafe)
		}
	}
}
