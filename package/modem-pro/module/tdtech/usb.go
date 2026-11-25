package tdtech

import "strings"

// ParseUSBMode accepts both reply forms in §11.8 and skips unrelated URCs.
func ParseUSBMode(lines []string) (string, bool) {
	for _, line := range lines {
		value := strings.TrimSpace(line)
		if raw, ok := strings.CutPrefix(value, "^SETMODE:"); ok {
			value = strings.TrimSpace(raw)
		}
		if USBModeName(value) != "" {
			return value, true
		}
	}
	return "", false
}

func USBModeName(value string) string {
	switch value {
	case "0", "2":
		return "ECM"
	case "1", "3", "4", "5":
		return "NCM"
	case "6":
		return "RNDIS"
	case "7":
		return "MBIM"
	case "8":
		return "PPP"
	default:
		return ""
	}
}
