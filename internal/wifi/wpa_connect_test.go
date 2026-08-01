package wifi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newFakeWpaEnv builds fake systemctl/wpa_cli executables and puts
// them on PATH, so wpaBackend.Connect can be exercised end-to-end
// without real network hardware -- confirms the actual bug found on
// real HamVoIP hardware (save_config running after select_network
// could strand the device off a working network) can't come back.
// systemctl always reports "active" (skips the start-service path
// entirely); wpa_cli logs every invocation to a file this test reads
// back afterward, and answers "status" according to associate
// (simulating whether the new network actually associates).
func newFakeWpaEnv(t *testing.T, associate bool, ssid string) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake wpa_cli/systemctl fixtures are POSIX shell scripts")
	}
	dir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "wpa_cli.log")

	systemctlScript := "#!/bin/sh\n" +
		"if [ \"$1\" = \"is-active\" ]; then echo active; exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(systemctlScript), 0755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}

	wpaCliScript := "#!/bin/sh\n" +
		"echo \"$*\" >> \"$FAKE_WPA_LOG\"\n" +
		"shift 2\n" +
		"case \"$1\" in\n" +
		"  add_network) echo 0 ;;\n" +
		"  status)\n" +
		"    if [ \"$FAKE_WPA_ASSOCIATE\" = \"1\" ]; then printf 'wpa_state=COMPLETED\\nssid=%s\\n' \"$FAKE_WPA_SSID\"; else echo wpa_state=DISCONNECTED; fi ;;\n" +
		"  scan_results) printf 'bssid\\tfrequency\\tsignal level\\tflags\\tssid\\n' ;;\n" +
		"  *) echo OK ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "wpa_cli"), []byte(wpaCliScript), 0755); err != nil {
		t.Fatalf("write fake wpa_cli: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_WPA_LOG", logPath)
	if associate {
		t.Setenv("FAKE_WPA_ASSOCIATE", "1")
	} else {
		t.Setenv("FAKE_WPA_ASSOCIATE", "0")
	}
	t.Setenv("FAKE_WPA_SSID", ssid)
	return logPath
}

func readWpaCliLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read wpa_cli log: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func firstIndexContaining(lines []string, substr string) int {
	for i, l := range lines {
		if strings.Contains(l, substr) {
			return i
		}
	}
	return -1
}

// TestConnectSavesConfigBeforeSwitchingNetworks is the direct
// regression test for a real incident: the previous ordering called
// select_network (which disables every *other* configured network)
// before save_config, so a save_config failure still left the device
// switched off a working network with nothing persisted to fall back
// to.
func TestConnectSavesConfigBeforeSwitchingNetworks(t *testing.T) {
	logPath := newFakeWpaEnv(t, true, "NewNetwork")
	b := newWpaBackend()
	if err := b.Connect(context.Background(), "NewNetwork", "somepassword1"); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	log := readWpaCliLog(t, logPath)
	saveIdx := firstIndexContaining(log, "save_config")
	selectIdx := firstIndexContaining(log, "select_network")
	if saveIdx == -1 {
		t.Fatal("save_config was never called")
	}
	if selectIdx == -1 {
		t.Fatal("select_network was never called")
	}
	if saveIdx > selectIdx {
		t.Errorf("save_config ran after select_network (indices %d, %d) -- a failed save_config could strand the device off a working network", saveIdx, selectIdx)
	}
}

func TestConnectReEnablesAllNetworksWhenAssociationFails(t *testing.T) {
	orig := associationTimeout
	associationTimeout = 200 * time.Millisecond
	defer func() { associationTimeout = orig }()

	logPath := newFakeWpaEnv(t, false, "NewNetwork")
	b := newWpaBackend()
	err := b.Connect(context.Background(), "NewNetwork", "somepassword1")
	if err == nil {
		t.Fatal("Connect() error = nil, want an error when the network never associates")
	}
	if !strings.Contains(err.Error(), "did not connect") {
		t.Errorf("Connect() error = %q, want it to explain the association failure", err.Error())
	}
	log := readWpaCliLog(t, logPath)
	if firstIndexContaining(log, "enable_network all") == -1 {
		t.Error("enable_network all was never called -- a bad new network would leave the device stranded with no working fallback")
	}
}

func TestConnectSucceedsWhenAssociationConfirmed(t *testing.T) {
	newFakeWpaEnv(t, true, "NewNetwork")
	b := newWpaBackend()
	if err := b.Connect(context.Background(), "NewNetwork", "somepassword1"); err != nil {
		t.Fatalf("Connect() error = %v, want nil once association is confirmed", err)
	}
}
