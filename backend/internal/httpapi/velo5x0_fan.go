package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	velo5x0FanConfigPath  = "etc/ly-route/velo5x0-fan.json"
	velo5x0FanStatusPath  = "run/ly-route/velo5x0-fan-status.json"
	velo5x0FanConfigLimit = 16 << 10
	velo5x0FanStatusLimit = 64 << 10
)

var velo5x0FanSensorPattern = regexp.MustCompile(`^temp[0-9]+_input$`)

type Velo5x0FanCurvePoint struct {
	Temperature float64 `json:"temperature"`
	PWM         int     `json:"pwm"`
}

type Velo5x0FanConfig struct {
	Mode             string                 `json:"mode"`
	ManualPWM        int                    `json:"manual_pwm"`
	TempSource       string                 `json:"temp_source"`
	CPUStatistic     string                 `json:"cpu_statistic"`
	CPUSensor        string                 `json:"cpu_sensor"`
	CurveProfile     string                 `json:"curve_profile"`
	MinPWM           int                    `json:"min_pwm"`
	StopTemperature  float64                `json:"stop_temperature"`
	StartTemperature float64                `json:"start_temperature"`
	FullTemperature  float64                `json:"full_temperature"`
	PollInterval     int                    `json:"poll_interval"`
	Curve            []Velo5x0FanCurvePoint `json:"curve"`
}

type Velo5x0FanTemperatures struct {
	CPU   *float64 `json:"cpu"`
	WiFi  *float64 `json:"wifi"`
	Board *float64 `json:"board"`
}

type Velo5x0FanSensor struct {
	Source string  `json:"source"`
	Sensor string  `json:"sensor"`
	Label  string  `json:"label"`
	Value  float64 `json:"value"`
}

type Velo5x0FanStatus struct {
	Running              bool                   `json:"running"`
	OutputPWM            *float64               `json:"output_pwm"`
	Temperatures         Velo5x0FanTemperatures `json:"temperatures"`
	Sensors              []Velo5x0FanSensor     `json:"sensors"`
	EffectiveTemperature *float64               `json:"effective_temperature"`
	Error                string                 `json:"error"`
	Mode                 string                 `json:"mode"`
	ConfigRevision       string                 `json:"config_revision"`
	UpdatedAt            int64                  `json:"updated_at"`
}

type Velo5x0FanResponse struct {
	Available bool             `json:"available"`
	Config    Velo5x0FanConfig `json:"config"`
	Status    Velo5x0FanStatus `json:"status"`
}

type velo5x0FanAPI struct {
	mu      sync.Mutex
	root    string
	rootErr error
}

// WithVelo5x0FanRoot redirects only the fixed fan filesystem paths, not HTTP input.
func WithVelo5x0FanRoot(root string) Option {
	return func(server *Server) { server.velo5x0Fan = newVelo5x0FanAPI(root) }
}

func newVelo5x0FanAPI(root string) *velo5x0FanAPI {
	if root == "" {
		root = string(filepath.Separator)
	}
	api := &velo5x0FanAPI{root: root}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsRune(root, 0) {
		api.rootErr = errors.New("fan filesystem root must be a clean absolute directory")
	}
	return api
}

func defaultVelo5x0FanConfig() Velo5x0FanConfig {
	return Velo5x0FanConfig{
		Mode: "curve", ManualPWM: 31, TempSource: "cpu",
		CPUStatistic: "max", CPUSensor: "temp2_input", CurveProfile: "linear",
		MinPWM: 16, StopTemperature: 42, StartTemperature: 45,
		FullTemperature: 60, PollInterval: 3,
		Curve: []Velo5x0FanCurvePoint{{45, 16}, {50, 43}, {55, 71}, {60, 100}},
	}
}

func (config Velo5x0FanConfig) validate() error {
	if config.Mode != "curve" && config.Mode != "manual" {
		return errors.New("mode must be curve or manual")
	}
	switch config.TempSource {
	case "cpu", "wifi", "board", "max", "average":
	default:
		return errors.New("temp_source must be cpu, wifi, board, max or average")
	}
	if config.CPUStatistic != "max" && config.CPUStatistic != "average" && config.CPUStatistic != "single" {
		return errors.New("cpu_statistic must be max, average or single")
	}
	if !velo5x0FanSensorPattern.MatchString(config.CPUSensor) {
		return errors.New("cpu_sensor must match temp[0-9]+_input")
	}
	if config.CurveProfile != "linear" && config.CurveProfile != "custom" {
		return errors.New("curve_profile must be linear or custom")
	}
	if config.ManualPWM < 0 || config.ManualPWM > 100 || config.MinPWM < 0 || config.MinPWM > 100 {
		return errors.New("manual_pwm and min_pwm must be integers from 0 to 100")
	}
	if !fanTemperatureValid(config.StopTemperature) || !fanTemperatureValid(config.StartTemperature) || !fanTemperatureValid(config.FullTemperature) {
		return errors.New("temperature thresholds must be finite numbers from 0 to 100")
	}
	if config.StopTemperature != 0 && config.StopTemperature >= config.StartTemperature {
		return errors.New("stop_temperature must be 0 or less than start_temperature")
	}
	if config.FullTemperature <= config.StartTemperature {
		return errors.New("full_temperature must be greater than start_temperature")
	}
	if config.PollInterval < 1 || config.PollInterval > 30 {
		return errors.New("poll_interval must be an integer from 1 to 30")
	}
	if len(config.Curve) != 4 {
		return errors.New("curve must contain exactly four points")
	}
	for index, point := range config.Curve {
		if !fanTemperatureValid(point.Temperature) || point.PWM < 0 || point.PWM > 100 {
			return errors.New("curve temperatures must be finite numbers from 0 to 100 and pwm must be an integer from 0 to 100")
		}
		if index > 0 && point.Temperature <= config.Curve[index-1].Temperature {
			return errors.New("curve temperatures must be strictly increasing")
		}
	}
	return nil
}

func fanTemperatureValid(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func decodeVelo5x0FanConfig(content []byte) (Velo5x0FanConfig, error) {
	config := defaultVelo5x0FanConfig()
	// Token validation prevents duplicate fields and explicit nulls from silently
	// replacing values or being mistaken for omitted fields with defaults.
	tokens := json.NewDecoder(bytes.NewReader(content))
	if err := validateFanJSONValue(tokens, true); err != nil {
		return config, err
	}
	if _, err := tokens.Token(); err != io.EOF {
		return config, errors.New("config must contain exactly one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil {
		return config, err
	}
	if raw, exists := object["curve"]; exists {
		var points []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &points); err != nil {
			return config, err
		}
		for _, point := range points {
			if len(point) != 2 || point["temperature"] == nil || point["pwm"] == nil {
				return config, errors.New("each curve point must contain temperature and pwm")
			}
		}
	}
	return config, config.validate()
}

func validateFanJSONValue(decoder *json.Decoder, requireObject bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("config fields cannot be null")
	}
	delimiter, compound := token.(json.Delim)
	if requireObject && (!compound || delimiter != '{') {
		return errors.New("config must be a JSON object")
	}
	if !compound {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return errors.New("invalid JSON value")
	}
	keys := map[string]bool{}
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return errors.New("config contains a duplicate or invalid field")
			}
			keys[name] = true
		}
		if err := validateFanJSONValue(decoder, false); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func (server *Server) handleVelo5x0Fan(w http.ResponseWriter, r *http.Request) {
	session, authenticated := server.sessionFromRequest(r)
	if !authenticated {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if r.Method == http.MethodPut {
		if server.passwordChangeRequired(w, r, session, "system_fan", "update") {
			return
		}
		if session.Role != "admin" {
			server.recordAudit(session.Username, session.Role, "system_fan", "update", "denied", "admin role required", r)
			writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
	}
	api := server.velo5x0Fan
	api.mu.Lock()
	defer api.mu.Unlock()
	available := api.available()
	if !available {
		if r.Method == http.MethodPut {
			writeError(w, r, http.StatusNotFound, "fan_unavailable", "fan control is unavailable on this hardware")
			return
		}
		writeJSON(w, http.StatusOK, Velo5x0FanResponse{
			Config: defaultVelo5x0FanConfig(),
			Status: emptyVelo5x0FanStatus("fan control is unavailable on this hardware"),
		})
		return
	}
	if r.Method == http.MethodPut {
		content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, velo5x0FanConfigLimit))
		if err != nil {
			var sizeError *http.MaxBytesError
			if errors.As(err, &sizeError) {
				writeError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "fan config exceeds 16 KiB")
			} else {
				writeError(w, r, http.StatusBadRequest, "invalid_json", "fan config could not be read")
			}
			return
		}
		config, err := decodeVelo5x0FanConfig(content)
		if err != nil {
			server.recordAudit(session.Username, session.Role, "system_fan", "update", "failed", err.Error(), r)
			writeError(w, r, http.StatusBadRequest, "invalid_fan_config", err.Error())
			return
		}
		if err := api.saveConfig(config); err != nil {
			server.recordAudit(session.Username, session.Role, "system_fan", "update", "failed", "fan config persistence failed", r)
			writeError(w, r, http.StatusInternalServerError, "fan_config_write_failed", "fan config could not be persisted")
			return
		}
		server.recordAudit(session.Username, session.Role, "system_fan", "update", "success", "", r)
		writeJSON(w, http.StatusOK, Velo5x0FanResponse{
			Available: true, Config: config, Status: api.status(config, server.now()),
		})
		return
	}
	config, err := api.config()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "fan_config_read_failed", "persisted fan config is unavailable or invalid")
		return
	}
	writeJSON(w, http.StatusOK, Velo5x0FanResponse{
		Available: true, Config: config, Status: api.status(config, server.now()),
	})
}

func (api *velo5x0FanAPI) available() bool {
	hardware, err := api.readFile("etc/ly-route/hardware", 1024, false)
	if err != nil || strings.TrimSpace(string(hardware)) != "velo5x0" {
		return false
	}
	// /sys/class/dmi/id normally aliases this canonical sysfs directory.
	for _, path := range []string{"sys/devices/virtual/dmi/id/board_name", "sys/class/dmi/id/board_name"} {
		board, err := api.readFile(path, 1024, false)
		if err == nil {
			name := strings.TrimSpace(string(board))
			return name == "EDGE520" || name == "EDGE540"
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	return false
}

func (api *velo5x0FanAPI) config() (Velo5x0FanConfig, error) {
	content, err := api.readFile(velo5x0FanConfigPath, velo5x0FanConfigLimit, true)
	if errors.Is(err, os.ErrNotExist) {
		return defaultVelo5x0FanConfig(), nil
	}
	if err != nil {
		return Velo5x0FanConfig{}, err
	}
	return decodeVelo5x0FanConfig(content)
}

func emptyVelo5x0FanStatus(reason string) Velo5x0FanStatus {
	return Velo5x0FanStatus{Sensors: []Velo5x0FanSensor{}, Error: reason, ConfigRevision: "default"}
}

func (api *velo5x0FanAPI) status(config Velo5x0FanConfig, now time.Time) Velo5x0FanStatus {
	content, err := api.readFile(velo5x0FanStatusPath, velo5x0FanStatusLimit, false)
	if err != nil {
		return emptyVelo5x0FanStatus("fan daemon status is unavailable")
	}
	var status Velo5x0FanStatus
	if err := json.Unmarshal(content, &status); err != nil || !validVelo5x0FanStatus(status) {
		return emptyVelo5x0FanStatus("fan daemon status is invalid")
	}
	if status.Sensors == nil {
		status.Sensors = []Velo5x0FanSensor{}
	}
	age := now.Unix() - status.UpdatedAt
	if status.UpdatedAt <= 0 || age < 0 || age > int64(2*config.PollInterval+5) {
		status.Running = false
		status.OutputPWM = nil
		status.Temperatures = Velo5x0FanTemperatures{}
		status.Sensors = []Velo5x0FanSensor{}
		status.EffectiveTemperature = nil
		status.Error = strings.TrimSpace(status.Error + " fan daemon status is stale")
	}
	return status
}

func validVelo5x0FanStatus(status Velo5x0FanStatus) bool {
	if status.Mode != "curve" && status.Mode != "manual" {
		return false
	}
	if status.ConfigRevision != "" && status.ConfigRevision != "default" {
		if len(status.ConfigRevision) != 64 {
			return false
		}
		for _, character := range status.ConfigRevision {
			if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
				return false
			}
		}
	}
	if status.OutputPWM != nil && !fanTemperatureValid(*status.OutputPWM) {
		return false
	}
	for _, value := range []*float64{status.Temperatures.CPU, status.Temperatures.WiFi, status.Temperatures.Board, status.EffectiveTemperature} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return false
		}
	}
	for _, sensor := range status.Sensors {
		if sensor.Source != "cpu" && sensor.Source != "wifi" && sensor.Source != "board" {
			return false
		}
		if math.IsNaN(sensor.Value) || math.IsInf(sensor.Value, 0) {
			return false
		}
	}
	return true
}

func (api *velo5x0FanAPI) safePath(relative string, createParents bool) (string, error) {
	if api.rootErr != nil {
		return "", api.rootErr
	}
	info, err := os.Lstat(api.root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("fan filesystem root is not a safe directory")
	}
	path := api.root
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `\/:`) {
			return "", errors.New("invalid fixed fan path")
		}
		path = filepath.Join(path, part)
		info, err = os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && createParents && index < len(parts)-1 {
			if err := os.Mkdir(path, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(path)
		}
		if errors.Is(err, os.ErrNotExist) && index == len(parts)-1 {
			return path, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return "", errors.New("fan path must not contain symlinks or writable shared files")
		}
		if index < len(parts)-1 && !info.IsDir() || index == len(parts)-1 && !info.Mode().IsRegular() {
			return "", errors.New("fan path has an unexpected file type")
		}
	}
	return path, nil
}

func (api *velo5x0FanAPI) readFile(relative string, limit int64, private bool) ([]byte, error) {
	path, err := api.safePath(relative, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if private && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("fan config permissions must be private")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("fan file changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errors.New("fan file exceeds its size limit")
	}
	return content, nil
}

func (api *velo5x0FanAPI) saveConfig(config Velo5x0FanConfig) error {
	if err := config.validate(); err != nil {
		return err
	}
	content, err := json.Marshal(config)
	if err != nil {
		return err
	}
	content = append(content, '\n')
	path, err := api.safePath(velo5x0FanConfigPath, true)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".velo5x0-fan-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	writeErr := file.Chmod(0o600)
	if writeErr == nil {
		_, writeErr = file.Write(content)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	if _, err := api.safePath(velo5x0FanConfigPath, false); err != nil {
		return err
	}
	// All fallible preparation happens before rename, so a failed write leaves
	// the previous config intact. The daemon rereads this file on its next poll.
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("atomically replace fan config: %w", err)
	}
	return nil
}
