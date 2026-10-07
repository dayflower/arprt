// Package byos contains the firmware contract, without Kubernetes dependencies.
package byos

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/dayflower/arprt/internal/render"
)

type Config struct {
	Namespace         string
	PublicBaseURL     string
	ProxyStripsPrefix bool
	EnableSetup       bool
	RefreshSeconds    int
}

func (c Config) Base() (*url.URL, error) {
	u, err := url.Parse(c.PublicBaseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.Contains(c.PublicBaseURL, "#") {
		return nil, errors.New("public base URL must be absolute HTTP(S) without query, fragment, userinfo or escaped path")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if !CleanPath(u.Path) {
		return nil, errors.New("public base URL has a noncanonical path")
	}
	return u, nil
}

func CleanPath(path string) bool {
	if path == "" {
		return true
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "//") || strings.ContainsAny(path, "\\%?#") {
		return false
	}
	for _, ch := range path {
		if !(ch >= 'a' && ch <= 'z') && !(ch >= 'A' && ch <= 'Z') && !(ch >= '0' && ch <= '9') && !strings.ContainsRune("/-._~", ch) {
			return false
		}
	}
	for _, s := range strings.Split(path[1:], "/") {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	return true
}

// Headers merges legacy underscore spelling; duplicates never choose a winner.
func Headers(h http.Header) (map[string]string, error) {
	out := map[string]string{}
	for name, values := range h {
		key := strings.ToLower(strings.ReplaceAll(name, "_", "-"))
		if _, found := out[key]; found || len(values) != 1 {
			if key == "id" || key == "access-token" {
				return nil, errors.New("duplicate identity or token header")
			}
			out[key] = "\x00"
			continue
		}
		out[key] = strings.TrimSpace(values[0])
	}
	return out, nil
}

var mac = regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){5}$`)

func HardwareID(value string) (string, error) {
	value = strings.ToUpper(value)
	if !mac.MatchString(value) {
		return "", errors.New("invalid device identity")
	}
	return value, nil
}

type Observation struct {
	Model, Firmware string
	Width, Height   *int32
	Voltage         *float64
	RSSI            *int32
	Invalid         []string
}

func Observe(h map[string]string) Observation {
	o := Observation{Model: h["model"], Firmware: h["fw-version"]}
	if len(o.Model) > 64 || strings.ContainsAny(o.Model, "\x00\r\n") {
		o.Model = ""
		o.Invalid = append(o.Invalid, "Model")
	}
	if len(o.Firmware) > 64 || strings.ContainsAny(o.Firmware, "\x00\r\n") {
		o.Firmware = ""
		o.Invalid = append(o.Invalid, "FW-Version")
	}
	for _, field := range []string{"width", "height", "rssi", "refresh-rate", "wake-time"} {
		value, ok := h[field]
		if !ok {
			continue
		}
		v, err := strconv.ParseInt(value, 10, 32)
		if err != nil || (field != "rssi" && v <= 0) {
			o.Invalid = append(o.Invalid, field)
			continue
		}
		n := int32(v)
		switch field {
		case "width":
			o.Width = &n
		case "height":
			o.Height = &n
		case "rssi":
			o.RSSI = &n
		}
	}
	if value, ok := h["battery-voltage"]; ok {
		v, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			o.Invalid = append(o.Invalid, "battery-voltage")
		} else {
			o.Voltage = &v
		}
	}
	for _, field := range []string{"image-cached", "usb-connected", "battery-charging"} {
		value, ok := h[field]
		if !ok {
			continue
		}
		valid := value == "true" || value == "false"
		if field == "battery-charging" {
			valid = value == "0" || value == "1"
		}
		if !valid {
			o.Invalid = append(o.Invalid, field)
		}
	}
	return o
}

// Target supports only the reviewed tuple. Missing observations can use known
// state, but a partial changed tuple cannot borrow the other half of the old one.
func (o Observation) Target(knownModel, knownFirmware string) (render.Target, string, string, error) {
	model, fw := o.Model, o.Firmware
	changed := (model != "" && model != knownModel) || (fw != "" && fw != knownFirmware)
	if !changed {
		if model == "" {
			model = knownModel
		}
		if fw == "" {
			fw = knownFirmware
		}
	}
	for _, f := range o.Invalid {
		if f == "width" || f == "height" || f == "Model" || f == "FW-Version" {
			return render.Target{}, model, fw, errors.New("invalid capability observation")
		}
	}
	if model != "og" || fw != "1.8.17" {
		return render.Target{}, model, fw, errors.New("unverified model/firmware tuple")
	}
	t := render.Target{Width: 800, Height: 480, Palette: "gray4"}
	if (o.Width != nil && *o.Width != t.Width) || (o.Height != nil && *o.Height != t.Height) {
		return render.Target{}, model, fw, errors.New("dimensions contradict verified capabilities")
	}
	return t, model, fw, nil
}

type DisplayResponse struct {
	Status          int    `json:"status"`
	ImageURL        string `json:"image_url"`
	Filename        string `json:"filename"`
	RefreshRate     int    `json:"refresh_rate"`
	ImageURLTimeout int    `json:"image_url_timeout"`
	UpdateFirmware  bool   `json:"update_firmware"`
	ResetFirmware   bool   `json:"reset_firmware"`
	SpecialFunction string `json:"special_function"`
}
