// Package wifi manages this node's wlan0 interface: scanning for
// nearby networks, connecting to one, and — the important recovery
// case — automatically standing up a fallback WiFi hotspot the moment
// the node has no active network connection at all (neither Ethernet
// nor WiFi), so it's never truly unreachable. See Manager's own doc
// comment for the automatic-hotspot state machine.
//
// Two backends are supported, auto-detected at runtime (see
// DetectBackend): NetworkManager (nmcli), the default on newer
// Raspberry Pi OS/Arch Linux ARM images, and dhcpcd+wpa_supplicant
// (wpa_cli), the stack internal/netconfig's own static-IP handling
// already assumes. Every command either backend runs follows
// internal/system's own convention exactly: exec.CommandContext with
// explicit, separate argv elements (never a shell string), a
// context.WithTimeout on every call, and stderr captured and %w-wrapped
// into the returned error.
package wifi

import (
	"context"
	"errors"
)

// wlan0Iface is hardcoded, not operator-configurable — same reasoning
// as defaultNetInterface in internal/server/system_settings.go: this
// app manages exactly one WiFi radio, and getting the interface name
// wrong here has no graceful failure mode.
const wlan0Iface = "wlan0"

// Mode is the current state of wlan0.
type Mode string

const (
	ModeUnavailable  Mode = "unavailable"  // no supported backend detected
	ModeDisconnected Mode = "disconnected" // backend present, wlan0 idle
	ModeClient       Mode = "client"       // associated to a real access point
	ModeHotspot      Mode = "hotspot"      // broadcasting this node's own fallback hotspot
)

// Network is one scan result.
type Network struct {
	SSID     string
	Signal   int    // 0-100, normalized across backends -- see nmcli.go/wpa.go
	Security string // "Open", "WEP", "WPA2", "WPA3"
}

// Status is wlan0's current state.
type Status struct {
	Mode      Mode
	SSID      string // associated/broadcast SSID; "" if Mode is Disconnected/Unavailable
	IPAddress string
}

// Backend is the network-stack-specific implementation of every WiFi
// operation this package offers — see nmcli.go and wpa.go for the two
// real implementations, and unavailableBackend below for what's used
// when neither stack is detected.
type Backend interface {
	// Name identifies which backend this is, for display on the System
	// page (e.g. "NetworkManager" or "wpa_supplicant/dhcpcd").
	Name() string
	Scan(ctx context.Context) ([]Network, error)
	// Connect associates wlan0 with ssid. psk == "" means an open
	// network.
	Connect(ctx context.Context, ssid, psk string) error
	Status(ctx context.Context) (Status, error)
	// StartHotspot begins broadcasting ssid/psk as this node's own
	// access point on wlan0, taking over from whatever client-mode
	// association was active. psk must be non-empty — see
	// ValidatePSK.
	StartHotspot(ctx context.Context, ssid, psk string) error
	// StopHotspot ends the hotspot and hands wlan0 back to normal
	// client-mode operation.
	StopHotspot(ctx context.Context) error
}

// ErrUnavailable is returned by every unavailableBackend method —
// callers check for it with errors.Is rather than needing a nil
// Backend check anywhere.
var ErrUnavailable = errors.New("wifi: no supported network backend detected on this system")

// unavailableBackend is what DetectBackend returns when neither
// NetworkManager nor wpa_supplicant/dhcpcd is present. Every method
// fails the same way, so callers (handlers, Manager) never need a nil
// check — they just get ErrUnavailable back, and the System page shows
// "WiFi management isn't available on this system" instead of
// crashing.
type unavailableBackend struct{}

func (unavailableBackend) Name() string { return "unavailable" }
func (unavailableBackend) Scan(context.Context) ([]Network, error) {
	return nil, ErrUnavailable
}
func (unavailableBackend) Connect(context.Context, string, string) error {
	return ErrUnavailable
}
func (unavailableBackend) Status(context.Context) (Status, error) {
	return Status{Mode: ModeUnavailable}, ErrUnavailable
}
func (unavailableBackend) StartHotspot(context.Context, string, string) error {
	return ErrUnavailable
}
func (unavailableBackend) StopHotspot(context.Context) error {
	return ErrUnavailable
}
