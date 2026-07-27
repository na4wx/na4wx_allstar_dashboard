package cloudagent

import (
	"context"
	"encoding/json"
	"fmt"

	"hamvoipconfiggui/internal/rptstatus"
	"hamvoipconfiggui/internal/system"
)

type nodeStatsParams struct {
	Number string `json:"number"`
}

// nodeStatsResult is a live-only counterpart to the local Stats page's
// per-node "Node N stats" card -- the full "rpt stats" field dump plus
// who's connected right now. Deliberately has no history array (unlike
// the local app's own linkHistory, sampled by an always-on 30s
// background poller) -- see this feature's plan doc for why: this
// package has no *Server to borrow that poller/buffer from without an
// import cycle, and giving every connected device's own cloudagent an
// unconditional 30s Asterisk-polling loop just for an optional history
// view is a real, permanent resource cost this pass isn't taking on.
// snapshotLiveNode (live.go) already covers Receiving/Connected for the
// existing "Live & Commands" tab; this action additionally surfaces the
// full field table the local Stats page shows, which that one doesn't.
type nodeStatsResult struct {
	StatsOK   bool                      `json:"statsOk"`
	Stats     []rptstatus.StatField     `json:"stats"`
	Receiving bool                      `json:"receiving"`
	Connected []rptstatus.ConnectedNode `json:"connected"`
}

// actionSystemNodeStats runs "rpt stats <number>" and "rpt nodes
// <number>" directly against this device's own Asterisk instance --
// mirrors internal/server/dashboard.go's gatherNodeStatuses (the exact
// same two commands, same rptstatus parsing helpers), minus the
// history recording that function also does.
func (a *Agent) actionSystemNodeStats(ctx context.Context, params json.RawMessage) (any, error) {
	var p nodeStatsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}

	if !a.validNodeNumber(p.Number) {
		return nil, fmt.Errorf("node %s not found", p.Number)
	}

	result := nodeStatsResult{Connected: []rptstatus.ConnectedNode{}}
	statsOut, err := system.AsteriskRX(ctx, a.asteriskBin, "rpt stats "+p.Number)
	if err != nil {
		return nil, fmt.Errorf("could not read node status: %w", err)
	}
	result.Stats, result.StatsOK = rptstatus.ParseRptStats(statsOut)
	result.Receiving = rptstatus.NodeReceiving(result.Stats)

	nodesOut, _ := system.AsteriskRX(ctx, a.asteriskBin, "rpt nodes "+p.Number)
	for _, num := range rptstatus.ParseConnectedNodes(nodesOut) {
		result.Connected = append(result.Connected, rptstatus.DescribeNode(nil, num))
	}
	if len(result.Connected) > 0 {
		if out, err := system.AsteriskRX(ctx, a.asteriskBin, "rpt show variables "+p.Number); err == nil {
			keyed := rptstatus.KeyedNodes(out)
			for i := range result.Connected {
				if keyed[result.Connected[i].Number] {
					result.Connected[i].Keyed = true
				}
			}
		}
	}

	return result, nil
}
