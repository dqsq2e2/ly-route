package httpapi

import (
	"os"
	"path/filepath"
	"strings"
)

func hostInterfacePortLabel(path, name, hardware string) (string, bool) {
	if hardware != "velo5x0" {
		return "", true
	}
	kind, err := os.ReadFile(filepath.Join(path, "type"))
	if err != nil || strings.TrimSpace(string(kind)) != "1" {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(path, "wireless")); err == nil {
		return "", false
	}
	if len(name) == 4 && strings.HasPrefix(name, "lan") && name[3] >= '1' && name[3] <= '8' {
		return strings.ToUpper(name), true
	}
	device, _ := filepath.EvalSymlinks(filepath.Join(path, "device"))
	pci := filepath.Base(device)
	// Exclude the internal switch links even when board initialization failed.
	if pci == "0000:00:14.0" || pci == "0000:00:14.1" {
		return "", false
	}
	for _, marker := range []string{"dsa", "upper_lan1", "upper_lan5"} {
		if _, err := os.Stat(filepath.Join(path, marker)); err == nil {
			return "", false
		}
	}
	switch pci {
	case "0000:00:14.2":
		return "GE1", true
	case "0000:00:14.3":
		return "GE2", true
	case "0000:04:00.0":
		return "SFP1", true
	case "0000:04:00.1":
		return "SFP2", true
	default:
		return "", true
	}
}
