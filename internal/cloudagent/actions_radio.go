package cloudagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"hamvoipconfiggui/internal/config"
)

// radioChannelRef and parseRadioChannel are a small, deliberate copy of
// internal/server/dashboard.go's own types of the same name -- this
// package can't import internal/server (server.go already constructs
// an Agent, so the reverse import would cycle), and the logic is short
// enough that duplicating it here is simpler and safer than a cross-
// cutting refactor of already-working local-app code just to share
// fifteen lines.
type radioChannelRef struct {
	File string
	Name string
}

func parseRadioChannel(channel string) (ref radioChannelRef, ok bool) {
	driver, name, found := strings.Cut(channel, "/")
	if !found || name == "" {
		return radioChannelRef{}, false
	}
	switch driver {
	case "USBRADIO":
		return radioChannelRef{File: config.UsbradioConfFile, Name: name}, true
	case "SimpleUSB":
		return radioChannelRef{File: config.SimpleusbConfFile, Name: name}, true
	default:
		return radioChannelRef{}, false
	}
}

// radioDeviceSummary is one entry in actionConfigListRadioDevices'
// result -- mirrors internal/server/system_settings.go's
// radioDeviceUsage, cross-referencing every configured node's RX/TX
// channel to say which node (if any) currently uses each device.
type radioDeviceSummary struct {
	File       string `json:"file"`
	Name       string `json:"name"`
	UsedByNode string `json:"usedByNode,omitempty"`
}

// actionConfigListRadioDevices lists every configured device across
// both driver files, same shape as the local System page's own "Radio
// devices" card.
func (a *Agent) actionConfigListRadioDevices(_ context.Context, _ json.RawMessage) (any, error) {
	var refs []radioDeviceSummary
	for _, file := range []string{config.UsbradioConfFile, config.SimpleusbConfFile} {
		names, err := a.store.ListRadioDevices(file)
		if err != nil {
			continue
		}
		for _, name := range names {
			refs = append(refs, radioDeviceSummary{File: file, Name: name})
		}
	}

	numbers, err := a.store.ListNodes()
	if err != nil {
		return refs, nil
	}
	usedBy := map[radioChannelRef]string{}
	for _, num := range numbers {
		node, err := a.store.LoadNode(num)
		if err != nil {
			continue
		}
		for _, ch := range []string{node.RXChannel, node.TXChannel} {
			if ref, ok := parseRadioChannel(ch); ok {
				usedBy[ref] = num
			}
		}
	}
	for i := range refs {
		if node, ok := usedBy[radioChannelRef{File: refs[i].File, Name: refs[i].Name}]; ok {
			refs[i].UsedByNode = node
		}
	}
	return refs, nil
}

type radioDeviceFileParams struct {
	File string `json:"file"`
	Name string `json:"name"`
}

// actionConfigLoadRadioDevice wraps config.Store.LoadRadioDevice.
func (a *Agent) actionConfigLoadRadioDevice(_ context.Context, params json.RawMessage) (any, error) {
	var p radioDeviceFileParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	return a.store.LoadRadioDevice(p.File, p.Name)
}

type radioDeviceSaveParams struct {
	File   string             `json:"file"`
	Device config.RadioDevice `json:"device"`
}

// actionConfigSaveRadioDevice wraps config.Store.SaveRadioDevice.
func (a *Agent) actionConfigSaveRadioDevice(_ context.Context, params json.RawMessage) (any, error) {
	var p radioDeviceSaveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	if err := a.store.SaveRadioDevice(p.File, &p.Device); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

// actionConfigDeleteRadioDevice wraps config.Store.DeleteRadioDevice.
func (a *Agent) actionConfigDeleteRadioDevice(_ context.Context, params json.RawMessage) (any, error) {
	var p radioDeviceFileParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	if err := a.store.DeleteRadioDevice(p.File, p.Name); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}
