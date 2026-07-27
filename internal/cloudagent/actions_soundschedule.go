package cloudagent

import (
	"context"
	"encoding/json"
	"fmt"

	"hamvoipconfiggui/internal/automation"
	"hamvoipconfiggui/internal/soundschedule"
)

type soundScheduleListParams struct {
	Node string `json:"node"`
}

// actionSoundScheduleList wraps soundschedule.Store.ListForNode.
func (a *Agent) actionSoundScheduleList(_ context.Context, params json.RawMessage) (any, error) {
	var p soundScheduleListParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	return a.soundSchedule.ListForNode(p.Node)
}

// validateSoundScheduleEntry mirrors internal/server/soundschedule.go's
// handleNodeAutomationSoundSave -- and goes further on Node/File, since
// this entry is later replayed by StartSoundSchedulePoller directly
// into system.RptPlayback/RptLocalPlay's own string-concatenated
// "rpt playback <node> <file>" AsteriskRX call (see that function's own
// doc comment): an unvalidated entry saved here doesn't just misbehave
// once, it re-fires on every matching tick until deleted. Node must be
// an existing rpt.conf section (same check as actionSystemDTMF/
// actionSystemNodeStats); File must be a real, known sound reference
// (not just non-blank, which is all the local handler itself checks --
// the cloud relay is a higher-trust boundary and holds itself to a
// stricter bar here, same reasoning as actions_dtmf.go's own comment).
func (a *Agent) validateSoundScheduleEntry(e soundschedule.Entry) error {
	if !a.validNodeNumber(e.Node) {
		return fmt.Errorf("node %s not found", e.Node)
	}
	files, err := a.sounds.ListAll()
	if err != nil {
		return fmt.Errorf("could not verify sound file: %w", err)
	}
	known := false
	for _, f := range files {
		if f.Ref == e.File {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("%q is not a known sound file", e.File)
	}
	for _, v := range []string{e.Minute, e.Hour, e.DayOfMonth, e.Month} {
		if !automation.TimeFieldRe.MatchString(v) {
			return fmt.Errorf("minute/hour/day-of-month/month must each be a single number or *")
		}
	}
	for _, wd := range e.DaysOfWeek {
		if wd < 0 || wd > 6 {
			return fmt.Errorf("invalid day-of-week value %d", wd)
		}
	}
	return nil
}

// actionSoundScheduleSave wraps soundschedule.Store.Save, params decoded
// directly as a soundschedule.Entry.
func (a *Agent) actionSoundScheduleSave(_ context.Context, params json.RawMessage) (any, error) {
	var e soundschedule.Entry
	if err := json.Unmarshal(params, &e); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	if e.Reach != soundschedule.ReachNetwork {
		e.Reach = soundschedule.ReachLocal
	}
	if err := a.validateSoundScheduleEntry(e); err != nil {
		return nil, err
	}
	if err := a.soundSchedule.Save(e); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

type soundScheduleDeleteParams struct {
	ID string `json:"id"`
}

// actionSoundScheduleDelete wraps soundschedule.Store.Delete.
func (a *Agent) actionSoundScheduleDelete(_ context.Context, params json.RawMessage) (any, error) {
	var p soundScheduleDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	if err := a.soundSchedule.Delete(p.ID); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}
