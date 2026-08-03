package wifi

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHostapdBinaryAt is the direct regression test for a real
// incident: on a node where install.sh had to build hostapd from
// source (because the packaged one was broken), the bare "hostapd"
// command still resolved to the old broken binary via PATH ordering,
// even after a working one existed at preferredHostapdPath.
func TestHostapdBinaryAt(t *testing.T) {
	t.Run("prefers the built binary when present", func(t *testing.T) {
		dir := t.TempDir()
		built := filepath.Join(dir, "hostapd")
		if err := os.WriteFile(built, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatalf("write fake built hostapd: %v", err)
		}
		if got := hostapdBinaryAt(built); got != built {
			t.Errorf("hostapdBinaryAt() = %q, want %q", got, built)
		}
	})

	t.Run("falls back to PATH lookup when not present", func(t *testing.T) {
		nonexistent := filepath.Join(t.TempDir(), "hostapd")
		if got := hostapdBinaryAt(nonexistent); got != "hostapd" {
			t.Errorf("hostapdBinaryAt() = %q, want \"hostapd\"", got)
		}
	})

	t.Run("falls back when the path is a directory, not a file", func(t *testing.T) {
		dir := t.TempDir()
		if got := hostapdBinaryAt(dir); got != "hostapd" {
			t.Errorf("hostapdBinaryAt() = %q, want \"hostapd\"", got)
		}
	})
}
