package cloudagent

import (
	"context"
	"encoding/json"
	"fmt"

	"hamvoipconfiggui/internal/config"
)

type recreateNodeDeviceParams struct {
	Number string `json:"number"`
}

type recreateNodeDeviceResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// actionConfigRecreateNodeDevice wraps the core repair logic of
// internal/server/dashboard.go's handleNodeRecreateDevice (minus the
// page-render/flash parts, which have no meaning relayed over the
// cloud): if a node's radio device is missing from its own config file
// entirely, recreate it with generic starting-point levels -- the
// operator's actual tuned audio levels can't be recovered once a
// stanza is gone.
func (a *Agent) actionConfigRecreateNodeDevice(_ context.Context, params json.RawMessage) (any, error) {
	var p recreateNodeDeviceParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}

	node, err := a.store.LoadNode(p.Number)
	if err != nil {
		return nil, fmt.Errorf("node %s not found", p.Number)
	}
	ref, ok := parseRadioChannel(node.RXChannel)
	if !ok {
		return recreateNodeDeviceResult{OK: false, Message: "node " + p.Number + "'s radio device isn't a recognized USB driver type — nothing to recreate"}, nil
	}
	if devices, err := a.store.ListRadioDevices(ref.File); err == nil {
		for _, d := range devices {
			if d == ref.Name {
				return recreateNodeDeviceResult{OK: true, Message: "device \"" + ref.Name + "\" already exists in " + ref.File + " — nothing to do"}, nil
			}
		}
	}

	if err := a.store.SaveRadioDevice(ref.File, placeholderRadioDevice(ref.Name)); err != nil {
		return nil, err
	}
	msg := "recreated device \"" + ref.Name + "\" in " + ref.File + " with generic starting-point levels (500/500) — the original tuned audio levels couldn't be recovered."
	return recreateNodeDeviceResult{OK: true, Message: msg}, nil
}

// placeholderRadioDevice is a deliberate copy of
// internal/server/system_settings.go's own function of the same name
// (same reasoning as parseRadioChannel in actions_radio.go -- this
// package can't import internal/server).
func placeholderRadioDevice(name string) *config.RadioDevice {
	return &config.RadioDevice{
		Name:        name,
		CarrierFrom: "usb",
		TXPrelim:    "yes",
		RXMixerSet:  "500",
		TXMixerSet:  "500",
	}
}

type syncExtensionsResult struct {
	Synced int      `json:"synced"`
	Total  int      `json:"total"`
	Failed []string `json:"failed,omitempty"`
}

// actionConfigSyncExtensions wraps the bulk loop from
// internal/server/dashboard.go's handleNodesSyncExtensions: backfills
// every configured node's extensions.conf dialplan entries in one
// pass. EnsureNodeExtensions only ever adds missing entries, never
// touches existing ones, so this is safe to run repeatedly.
func (a *Agent) actionConfigSyncExtensions(_ context.Context, _ json.RawMessage) (any, error) {
	numbers, err := a.store.ListNodes()
	if err != nil {
		return nil, err
	}

	var failed []string
	for _, number := range numbers {
		if err := a.store.EnsureNodeExtensions(number); err != nil {
			failed = append(failed, number+": "+err.Error())
		}
	}
	return syncExtensionsResult{Synced: len(numbers) - len(failed), Total: len(numbers), Failed: failed}, nil
}
