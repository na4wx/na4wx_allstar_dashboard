package cloudagent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"hamvoipconfiggui/internal/system"
)

// dtmfDigitsRe is deliberately stricter than the local app's own
// handleNodeSendDTMF, which has no character validation at all (fine
// for a LAN-only session where the operator is typing the digits
// themselves). The cloud relay is a higher-trust boundary, so this
// action set is more defensive than local, not merely as defensive —
// only the characters a real DTMF pad plus app_rpt's A-D extended
// digits can produce are ever allowed into the shell command built
// below.
var dtmfDigitsRe = regexp.MustCompile(`^[0-9*#A-Da-d]+$`)

type systemDTMFParams struct {
	Number string `json:"number"`
	Digits string `json:"digits"`
}

// actionSystemDTMF wraps system.AsteriskRX with "rpt fun <node>
// <digits>", exactly mirroring handleNodeSendDTMF's own command string
// — i.e. exactly what would happen if this sequence were dialed on the
// radio.
func (a *Agent) actionSystemDTMF(ctx context.Context, params json.RawMessage) (any, error) {
	var p systemDTMFParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	if !a.validNodeNumber(p.Number) {
		return nil, fmt.Errorf("node %s not found", p.Number)
	}
	digits := strings.TrimSpace(p.Digits)
	if !dtmfDigitsRe.MatchString(digits) {
		return nil, fmt.Errorf("digits must contain only 0-9, *, #, or A-D")
	}
	out, err := system.AsteriskRX(ctx, a.asteriskBin, "rpt fun "+p.Number+" "+digits)
	if err != nil {
		return nil, err
	}
	return map[string]string{"output": out}, nil
}
