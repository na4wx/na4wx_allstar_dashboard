package cloudagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hamvoipconfiggui/internal/config"
)

const testMaintenanceNodeConf = `[546051]
rxchannel = SimpleUSB/usb
`

func newMaintenanceTestAgent(t *testing.T, extraFiles map[string]string) *Agent {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rpt.conf"), []byte(testMaintenanceNodeConf), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	for name, content := range extraFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	return newTestAgent(t, filepath.Join(t.TempDir(), "settings.json"), config.NewStore(dir), "asterisk")
}

func TestActionConfigRecreateNodeDeviceWhenMissing(t *testing.T) {
	a := newMaintenanceTestAgent(t, nil) // no simpleusb.conf at all -- device is missing

	params, _ := json.Marshal(recreateNodeDeviceParams{Number: "546051"})
	result, err := a.dispatch(context.Background(), "config.recreateNodeDevice", params)
	if err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	res := result.(recreateNodeDeviceResult)
	if !res.OK {
		t.Errorf("res.OK = false, want true: %s", res.Message)
	}

	device, err := a.store.LoadRadioDevice(config.SimpleusbConfFile, "usb")
	if err != nil {
		t.Fatalf("LoadRadioDevice after recreate: %v", err)
	}
	if device.RXMixerSet != "500" || device.TXMixerSet != "500" {
		t.Errorf("recreated device = %+v, want placeholder 500/500 levels", device)
	}
}

func TestActionConfigRecreateNodeDeviceAlreadyExists(t *testing.T) {
	a := newMaintenanceTestAgent(t, map[string]string{
		config.SimpleusbConfFile: "[usb]\ncarrierfrom = usbinvert\n",
	})

	params, _ := json.Marshal(recreateNodeDeviceParams{Number: "546051"})
	result, err := a.dispatch(context.Background(), "config.recreateNodeDevice", params)
	if err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	res := result.(recreateNodeDeviceResult)
	if !res.OK {
		t.Errorf("res.OK = false, want true (already exists is a success no-op): %s", res.Message)
	}

	device, err := a.store.LoadRadioDevice(config.SimpleusbConfFile, "usb")
	if err != nil {
		t.Fatalf("LoadRadioDevice: %v", err)
	}
	if device.CarrierFrom != "usbinvert" {
		t.Errorf("CarrierFrom = %q, want the pre-existing value untouched", device.CarrierFrom)
	}
}

func TestActionConfigSyncExtensions(t *testing.T) {
	a := newMaintenanceTestAgent(t, nil)

	result, err := a.dispatch(context.Background(), "config.syncExtensions", nil)
	if err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	res := result.(syncExtensionsResult)
	if res.Total != 1 || res.Synced != 1 || len(res.Failed) != 0 {
		t.Errorf("res = %+v, want Total=1 Synced=1 no failures", res)
	}
}
