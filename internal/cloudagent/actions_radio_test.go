package cloudagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hamvoipconfiggui/internal/config"
)

const testRadioNodeConf = `[546051]
rxchannel = SimpleUSB/usb
`

const testRadioUsbConf = `[usb]
carrierfrom = usbinvert
rxmixerset = 500
txmixerset = 500
`

func newRadioTestAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.SimpleusbConfFile), []byte(testRadioUsbConf), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rpt.conf"), []byte(testRadioNodeConf), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return newTestAgent(t, filepath.Join(t.TempDir(), "settings.json"), config.NewStore(dir), "asterisk")
}

func TestActionConfigListRadioDevices(t *testing.T) {
	a := newRadioTestAgent(t)

	result, err := a.dispatch(context.Background(), "config.listRadioDevices", nil)
	if err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	refs, ok := result.([]radioDeviceSummary)
	if !ok {
		t.Fatalf("result type = %T, want []radioDeviceSummary", result)
	}
	if len(refs) != 1 || refs[0].File != config.SimpleusbConfFile || refs[0].Name != "usb" {
		t.Fatalf("refs = %+v, want one usb device in %s", refs, config.SimpleusbConfFile)
	}
	if refs[0].UsedByNode != "546051" {
		t.Errorf("UsedByNode = %q, want 546051 (node 546051's rxchannel references it)", refs[0].UsedByNode)
	}
}

func TestActionConfigLoadRadioDevice(t *testing.T) {
	a := newRadioTestAgent(t)

	params, _ := json.Marshal(radioDeviceFileParams{File: config.SimpleusbConfFile, Name: "usb"})
	result, err := a.dispatch(context.Background(), "config.loadRadioDevice", params)
	if err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	device, ok := result.(*config.RadioDevice)
	if !ok {
		t.Fatalf("result type = %T, want *config.RadioDevice", result)
	}
	if device.CarrierFrom != "usbinvert" || device.RXMixerSet != "500" {
		t.Errorf("device = %+v, want the fixture's values", device)
	}
}

func TestActionConfigSaveAndDeleteRadioDevice(t *testing.T) {
	a := newRadioTestAgent(t)

	saveParams, _ := json.Marshal(radioDeviceSaveParams{
		File:   config.SimpleusbConfFile,
		Device: config.RadioDevice{Name: "usb2", CarrierFrom: "usb", RXMixerSet: "400", TXMixerSet: "400"},
	})
	if _, err := a.dispatch(context.Background(), "config.saveRadioDevice", saveParams); err != nil {
		t.Fatalf("dispatch(save) error = %v", err)
	}

	loadParams, _ := json.Marshal(radioDeviceFileParams{File: config.SimpleusbConfFile, Name: "usb2"})
	result, err := a.dispatch(context.Background(), "config.loadRadioDevice", loadParams)
	if err != nil {
		t.Fatalf("dispatch(load after save) error = %v", err)
	}
	if device := result.(*config.RadioDevice); device.RXMixerSet != "400" {
		t.Errorf("RXMixerSet = %q, want 400 (just saved)", device.RXMixerSet)
	}

	if _, err := a.dispatch(context.Background(), "config.deleteRadioDevice", loadParams); err != nil {
		t.Fatalf("dispatch(delete) error = %v", err)
	}
	if _, err := a.dispatch(context.Background(), "config.loadRadioDevice", loadParams); err == nil {
		t.Error("dispatch(load after delete) error = nil, want an error since the device was just deleted")
	}
}
