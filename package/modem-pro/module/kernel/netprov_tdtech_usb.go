//go:build !windows

package kernel

import (
	"fmt"
	"os"
	"path/filepath"

	"modemd/module/sysfs"
	"modemd/module/usbdesc"
)

func (mod *Modem) prepareTDTechWANUSB() (bool, error) {
	if !mod.isMT5700MCN() || mod.DialStatus().ManualStop || !mod.ndisMu.TryLock() {
		return false, nil
	}
	defer mod.ndisMu.Unlock()
	if dialInProgress(mod) {
		return false, nil
	}

	dev := usbdesc.UsbDeviceDir(mod.ATPort)
	if dev == "" {
		return false, nil
	}
	if _, _, err := pickModemNetdev(mod); err == nil {
		return false, nil
	}
	info := mod.USBType(false)
	switch info.Value {
	case "0", "2", "4", "5":
		if info.Source == "at" {
			return enableTDTechDataUSBConfig(dev, info.Kind)
		}
	}
	return false, nil
}

// Some MT5700M-CN units boot in USB configuration 1 with only serial
// interfaces. Configuration 2 can expose a standard CDC data function.
// Check the device's descriptors before changing the host-side configuration.
func enableTDTechDataUSBConfig(dev, kind string) (bool, error) {
	var dataSubclass byte
	switch kind {
	case "ecm":
		dataSubclass = 0x06
	case "ncm":
		dataSubclass = 0x0d
	default:
		return false, nil
	}

	path := filepath.Join(dev, "bConfigurationValue")
	if sysfs.ReadSysfsID(filepath.Join(dev, "idVendor")) != "3466" ||
		sysfs.ReadSysfsID(filepath.Join(dev, "idProduct")) != "3301" ||
		sysfs.ReadSysfsLine(path) != "1" {
		return false, nil
	}
	descriptors, err := os.ReadFile(filepath.Join(dev, "descriptors"))
	if err != nil {
		return false, err
	}
	if !tdtechHasAlternateDataConfig(descriptors, dataSubclass) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte("2\n"), 0); err != nil {
		return false, fmt.Errorf("切换 MT5700M-CN 的 USB 配置：%w", err)
	}
	return true, nil
}

func tdtechHasAlternateDataConfig(descriptors []byte, dataSubclass byte) bool {
	var config byte
	var currentSeen, currentHasData, alternateControl, alternateData bool
	for len(descriptors) > 0 {
		if len(descriptors) < 2 || descriptors[0] < 2 || int(descriptors[0]) > len(descriptors) {
			return false
		}
		entry := descriptors[:descriptors[0]]
		descriptors = descriptors[descriptors[0]:]
		switch entry[1] {
		case 2: // USB configuration descriptor
			if len(entry) < 9 {
				return false
			}
			config = entry[5]
			currentSeen = currentSeen || config == 1
		case 4: // USB interface descriptor
			if len(entry) < 9 {
				return false
			}
			class, subclass, protocol := entry[5], entry[6], entry[7]
			if config == 1 && (class == 0x02 || class == 0x0a || class == 0xe0 ||
				(class == 0xff && subclass == 0x02 && protocol == 0x16)) {
				currentHasData = true
			}
			if config == 2 {
				alternateControl = alternateControl || (class == 0x02 && subclass == dataSubclass && protocol == 0)
				alternateData = alternateData || class == 0x0a
			}
		}
	}
	return currentSeen && !currentHasData && alternateControl && alternateData
}
