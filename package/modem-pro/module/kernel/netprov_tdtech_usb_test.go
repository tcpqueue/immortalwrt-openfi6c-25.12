//go:build !windows

package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTDTechAlternateUSBConfig(t *testing.T) {
	config := func(value byte) []byte {
		return []byte{9, 2, 9, 0, 1, value, 0, 0x80, 0}
	}
	intf := func(class, subclass, protocol byte) []byte {
		return []byte{9, 4, 0, 0, 0, class, subclass, protocol, 0}
	}
	serial := intf(0xff, 0x06, 0x12)
	ncmControl := intf(0x02, 0x0d, 0)
	cdcData := intf(0x0a, 0, 1)
	var descriptors []byte
	for _, descriptor := range [][]byte{config(1), serial, config(2), ncmControl, cdcData} {
		descriptors = append(descriptors, descriptor...)
	}
	if !tdtechHasAlternateDataConfig(descriptors, 0x0d) || tdtechHasAlternateDataConfig(descriptors, 0x06) {
		t.Fatal("the alternate configuration should only match NCM")
	}
	if tdtechHasAlternateDataConfig(append(append([]byte(nil), descriptors...), 1), 0x0d) {
		t.Fatal("malformed descriptor stream should not enable USB reconfiguration")
	}
	live := append(config(1), ncmControl...)
	live = append(live, descriptors[len(config(1)):]...)
	if tdtechHasAlternateDataConfig(live, 0x0d) {
		t.Fatal("a CDC interface already present in configuration 1 must be preserved")
	}
	dir := t.TempDir()
	for file, data := range map[string][]byte{
		"idVendor": []byte("3466\n"), "idProduct": []byte("3301\n"),
		"bConfigurationValue": []byte("1\n"), "descriptors": descriptors,
	} {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if changed, err := enableTDTechDataUSBConfig(dir, "ecm"); changed || err != nil {
		t.Fatalf("refusing ECM/NCM mismatch: %v, %v", changed, err)
	}
	if changed, err := enableTDTechDataUSBConfig(dir, "ncm"); !changed || err != nil {
		t.Fatalf("selecting configuration 2: %v, %v", changed, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "bConfigurationValue"))
	if err != nil || strings.TrimSpace(string(raw)) != "2" {
		t.Fatalf("configuration should be 2, got %q, %v", raw, err)
	}
	if changed, err := enableTDTechDataUSBConfig(dir, "ncm"); changed || err != nil {
		t.Fatalf("configuration 2 must be preserved: %v, %v", changed, err)
	}
}
