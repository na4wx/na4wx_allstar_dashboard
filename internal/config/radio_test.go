package config

import (
	"os"
	"path/filepath"
	"testing"
)

const testUsbradioConf = `[usb]
carrierfrom = usbinvert
ctcssfrom = no
rxdemod = speaker
txprelim = yes
txmixa = voice
invertptt = 0
rxmixerset = 500
txmixerset = 500
hdwtype = 0
`

func newRadioTestStore(t *testing.T, filename, content string) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return NewStore(dir)
}

func TestListRadioDevices(t *testing.T) {
	s := newRadioTestStore(t, UsbradioConfFile, testUsbradioConf)
	devices, err := s.ListRadioDevices(UsbradioConfFile)
	if err != nil {
		t.Fatalf("ListRadioDevices: %v", err)
	}
	if len(devices) != 1 || devices[0] != "usb" {
		t.Fatalf("ListRadioDevices = %v, want [usb]", devices)
	}
}

// Regression test for a real deployment finding: a stock HamVoIP node's
// simpleusb.conf has an empty [general] defaults section (standard
// Asterisk driver-config convention) alongside real device sections —
// [general] must not show up as a fake "device".
const testSimpleusbConfWithGeneral = `[general]

[usb]
carrierfrom = usbinvert
ctcssfrom = no
invertptt = 0
`

func TestListRadioDevicesExcludesGeneral(t *testing.T) {
	s := newRadioTestStore(t, SimpleusbConfFile, testSimpleusbConfWithGeneral)
	devices, err := s.ListRadioDevices(SimpleusbConfFile)
	if err != nil {
		t.Fatalf("ListRadioDevices: %v", err)
	}
	if len(devices) != 1 || devices[0] != "usb" {
		t.Fatalf("ListRadioDevices = %v, want [usb] (general excluded)", devices)
	}
}

func TestLoadRadioDeviceRejectsGeneral(t *testing.T) {
	s := newRadioTestStore(t, SimpleusbConfFile, testSimpleusbConfWithGeneral)
	if _, err := s.LoadRadioDevice(SimpleusbConfFile, "general"); err == nil {
		t.Fatalf("expected error loading [general] as a device")
	}
}

func TestSaveRadioDeviceRejectsGeneral(t *testing.T) {
	s := newRadioTestStore(t, SimpleusbConfFile, testSimpleusbConfWithGeneral)
	err := s.SaveRadioDevice(SimpleusbConfFile, &RadioDevice{Name: "general", CarrierFrom: "usb"})
	if err == nil {
		t.Fatalf("expected error saving a device named \"general\"")
	}
}

func TestApplyShariUSBPresetOverwritesOnlyDocumentedFields(t *testing.T) {
	d := &RadioDevice{
		Name:        "usb",
		CarrierFrom: "usb", // should be overwritten
		CTCSSFrom:   "dsp", // should be overwritten
		RXBoost:     "1",   // should be overwritten
		RXMixerSet:  "375", // must survive untouched
		TXMixerSet:  "500", // must survive untouched
	}
	ApplyShariUSBPreset(d)

	if d.CarrierFrom != "usbinvert" || d.CTCSSFrom != "no" || d.RXBoost != "0" {
		t.Fatalf("preset fields not applied: %+v", d)
	}
	if d.PreEmphasis != "1" || d.DeEmphasis != "1" || d.PLFilter != "1" {
		t.Fatalf("emphasis/filter fields not applied: %+v", d)
	}
	if d.RXMixerSet != "375" || d.TXMixerSet != "500" {
		t.Fatalf("preset should not touch audio levels, got: %+v", d)
	}
	if d.Name != "usb" {
		t.Fatalf("preset should not touch device name, got: %+v", d)
	}
	// Direct regression test for a real incident: on real SHARI hardware,
	// chan_simpleusb silently overwrites RXMixerSet/TXMixerSet from the
	// USB fob's own onboard EEPROM on every load unless EEPROM is
	// explicitly off, making a saved audio-level change appear to do
	// nothing at all.
	if d.EEPROM != "0" {
		t.Fatalf("preset should turn off EEPROM so audio levels actually take effect, got: %+v", d)
	}
}

func TestPreEmphasisDeEmphasisPLFilterRoundTrip(t *testing.T) {
	s := newRadioTestStore(t, SimpleusbConfFile, testUsbradioConf)
	d, err := s.LoadRadioDevice(SimpleusbConfFile, "usb")
	if err != nil {
		t.Fatalf("LoadRadioDevice: %v", err)
	}
	ApplyShariUSBPreset(d)
	if err := s.SaveRadioDevice(SimpleusbConfFile, d); err != nil {
		t.Fatalf("SaveRadioDevice: %v", err)
	}

	got, err := s.LoadRadioDevice(SimpleusbConfFile, "usb")
	if err != nil {
		t.Fatalf("LoadRadioDevice after save: %v", err)
	}
	if got.PreEmphasis != "1" || got.DeEmphasis != "1" || got.PLFilter != "1" {
		t.Fatalf("fields did not round-trip: %+v", got)
	}
	if got.EEPROM != "0" {
		t.Fatalf("EEPROM did not round-trip: %+v", got)
	}
}

// TestSaveRadioDeviceUpdatesExistingTuneFile is the direct regression
// test for a real incident: on real HamVoIP hardware, chan_simpleusb.c
// actually loads live RXMixerSet/TXMixerSet from a separate per-device
// "tune file" (simpleusb_tune_<name>.conf), not simpleusb.conf itself --
// confirmed by editing that exact file on a real node and observing the
// live value change after a restart. Saving a device whose tune file
// already exists must mirror the new values into it too (using its own
// txmixaset key name, not txmixerset), while leaving devstr and every
// other field in that file untouched.
func TestSaveRadioDeviceUpdatesExistingTuneFile(t *testing.T) {
	s := newRadioTestStore(t, SimpleusbConfFile, testUsbradioConf)
	tunePath := filepath.Join(s.dir, "simpleusb_tune_usb.conf")
	tuneContent := "[usb]\ndevstr=1-1.3:1.0\nrxmixerset=700\ntxmixaset=200\ntxmixbset=300\ntxdsplvl=999\n"
	if err := os.WriteFile(tunePath, []byte(tuneContent), 0644); err != nil {
		t.Fatalf("write tune fixture: %v", err)
	}

	d, err := s.LoadRadioDevice(SimpleusbConfFile, "usb")
	if err != nil {
		t.Fatalf("LoadRadioDevice: %v", err)
	}
	d.RXMixerSet = "350"
	d.TXMixerSet = "222"
	if err := s.SaveRadioDevice(SimpleusbConfFile, d); err != nil {
		t.Fatalf("SaveRadioDevice: %v", err)
	}

	got, err := os.ReadFile(tunePath)
	if err != nil {
		t.Fatalf("read tune file after save: %v", err)
	}
	tf, err := s.RawFile("simpleusb_tune_usb.conf")
	if err != nil {
		t.Fatalf("RawFile: %v", err)
	}
	if v, _ := tf.Get("usb", "rxmixerset"); v != "350" {
		t.Errorf("tune file rxmixerset = %q, want 350 (raw file: %s)", v, got)
	}
	if v, _ := tf.Get("usb", "txmixaset"); v != "222" {
		t.Errorf("tune file txmixaset = %q, want 222 (raw file: %s)", v, got)
	}
	if v, _ := tf.Get("usb", "devstr"); v != "1-1.3:1.0" {
		t.Errorf("tune file devstr = %q, want unchanged 1-1.3:1.0 (raw file: %s)", v, got)
	}
	if v, _ := tf.Get("usb", "txmixbset"); v != "300" {
		t.Errorf("tune file txmixbset = %q, want unchanged 300 (raw file: %s)", v, got)
	}
}

// TestSaveRadioDeviceDoesNotCreateTuneFile guards the other half of the
// same fix: this app must never create a device's tune file from
// scratch, since it has no way to correctly generate devstr (a USB
// bus-topology path specific to whichever physical port the device is
// plugged into) -- a device that's never been tuned via
// simpleusb-tune-menu should keep relying on the main conf file's own
// RXMixerSet/TXMixerSet.
func TestSaveRadioDeviceDoesNotCreateTuneFile(t *testing.T) {
	s := newRadioTestStore(t, SimpleusbConfFile, testUsbradioConf)
	d, err := s.LoadRadioDevice(SimpleusbConfFile, "usb")
	if err != nil {
		t.Fatalf("LoadRadioDevice: %v", err)
	}
	d.RXMixerSet = "350"
	if err := s.SaveRadioDevice(SimpleusbConfFile, d); err != nil {
		t.Fatalf("SaveRadioDevice: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "simpleusb_tune_usb.conf")); !os.IsNotExist(err) {
		t.Fatalf("SaveRadioDevice must not create a tune file from scratch, stat err = %v", err)
	}
}

func TestListRadioDevicesRejectsWrongFile(t *testing.T) {
	s := newRadioTestStore(t, UsbradioConfFile, testUsbradioConf)
	if _, err := s.ListRadioDevices("rpt.conf"); err == nil {
		t.Fatalf("expected error for non-radio file")
	}
}

func TestLoadRadioDevice(t *testing.T) {
	s := newRadioTestStore(t, UsbradioConfFile, testUsbradioConf)
	d, err := s.LoadRadioDevice(UsbradioConfFile, "usb")
	if err != nil {
		t.Fatalf("LoadRadioDevice: %v", err)
	}
	if d.CarrierFrom != "usbinvert" {
		t.Fatalf("CarrierFrom = %q", d.CarrierFrom)
	}
	if d.RXMixerSet != "500" {
		t.Fatalf("RXMixerSet = %q", d.RXMixerSet)
	}
	if d.HdwType != "0" {
		t.Fatalf("HdwType = %q", d.HdwType)
	}
}

func TestSaveRadioDeviceUpdatesExisting(t *testing.T) {
	s := newRadioTestStore(t, UsbradioConfFile, testUsbradioConf)
	d, err := s.LoadRadioDevice(UsbradioConfFile, "usb")
	if err != nil {
		t.Fatalf("LoadRadioDevice: %v", err)
	}
	d.RXMixerSet = "700"
	d.TXCTCSSDefault = "100.0" // previously unset
	if err := s.SaveRadioDevice(UsbradioConfFile, d); err != nil {
		t.Fatalf("SaveRadioDevice: %v", err)
	}

	d2, err := s.LoadRadioDevice(UsbradioConfFile, "usb")
	if err != nil {
		t.Fatalf("LoadRadioDevice after save: %v", err)
	}
	if d2.RXMixerSet != "700" {
		t.Fatalf("RXMixerSet after save = %q", d2.RXMixerSet)
	}
	if d2.TXCTCSSDefault != "100.0" {
		t.Fatalf("TXCTCSSDefault after save = %q", d2.TXCTCSSDefault)
	}
	if d2.CarrierFrom != "usbinvert" {
		t.Fatalf("untouched CarrierFrom = %q", d2.CarrierFrom)
	}
}

func TestSaveRadioDeviceCreatesNew(t *testing.T) {
	s := newRadioTestStore(t, UsbradioConfFile, testUsbradioConf)
	d := &RadioDevice{Name: "usb1", CarrierFrom: "dsp", RXMixerSet: "600"}
	if err := s.SaveRadioDevice(UsbradioConfFile, d); err != nil {
		t.Fatalf("SaveRadioDevice: %v", err)
	}
	devices, err := s.ListRadioDevices(UsbradioConfFile)
	if err != nil {
		t.Fatalf("ListRadioDevices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("ListRadioDevices = %v, want 2", devices)
	}
}

func TestDeleteRadioDevice(t *testing.T) {
	s := newRadioTestStore(t, UsbradioConfFile, testUsbradioConf)
	if err := s.DeleteRadioDevice(UsbradioConfFile, "usb"); err != nil {
		t.Fatalf("DeleteRadioDevice: %v", err)
	}
	devices, err := s.ListRadioDevices(UsbradioConfFile)
	if err != nil {
		t.Fatalf("ListRadioDevices: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("ListRadioDevices after delete = %v", devices)
	}
}

func TestSimpleusbConfSharesFieldMapping(t *testing.T) {
	s := newRadioTestStore(t, SimpleusbConfFile, testUsbradioConf)
	d, err := s.LoadRadioDevice(SimpleusbConfFile, "usb")
	if err != nil {
		t.Fatalf("LoadRadioDevice: %v", err)
	}
	if d.CarrierFrom != "usbinvert" {
		t.Fatalf("CarrierFrom = %q", d.CarrierFrom)
	}
}
