package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zarkmakerburg/baft/internal/agentjob"
	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

// maxOutput bounds the secret output a job may return (pairing codes).
const maxOutput = 16 << 10

// executeFull runs a job and also returns its secret output, which goes to
// BCC only in the ack and is never logged.
func (a *Agent) executeFull(ctx context.Context, j agentjob.Job) (detail, output string, err error) {
	if strings.HasPrefix(j.Action, "tunnel_") {
		return a.tunnel(ctx, j)
	}
	detail, err = a.execute(ctx, j)
	return detail, "", err
}

func (a *Agent) tunnel(ctx context.Context, j agentjob.Job) (string, string, error) {
	t := a.cfg.Tunnel
	if t == nil {
		return "", "", errors.New("tunnel changes are not enabled on this agent")
	}
	p, id := j.Params, j.Params["tunnel_id"]
	switch j.Action {
	case agentjob.ActionTunnelPrepareEX:
		port, _ := strconv.Atoi(p["port"])
		code, err := t.PrepareEX(ctx, id, exParams(p, port))
		if err != nil {
			return "", "", err
		}
		return "pairing code issued", code, nil
	case agentjob.ActionTunnelPrepareIR:
		reply, err := t.PrepareIR(ctx, id, p["code"], irParams(p))
		if err != nil {
			return "", "", err
		}
		return "reply code issued", reply, nil
	case agentjob.ActionTunnelCommitEX:
		d, err := t.CommitEX(ctx, id, p["reply"])
		return d, "", err
	case agentjob.ActionTunnelCommitIR:
		d, err := t.CommitIR(ctx, id)
		return d, "", err
	case agentjob.ActionTunnelHealth:
		d, err := t.Health(ctx, id)
		return d, "", err
	case agentjob.ActionTunnelObserve:
		o, err := t.Observe(ctx, id)
		if err != nil {
			return "", "", err
		}
		b, err := json.Marshal(o)
		return string(b), "", err
	case agentjob.ActionTunnelInspect:
		b, err := json.Marshal(t.Inspect(ctx))
		return string(b), "", err
	case agentjob.ActionTunnelFinalize:
		d, err := t.Finalize(ctx, id)
		return d, "", err
	case agentjob.ActionTunnelRollback:
		d, err := t.Rollback(ctx, id)
		return d, "", err
	}
	return "", "", fmt.Errorf("action %s has no tunnel handler", j.Action)
}

func exParams(p map[string]string, port int) tunnelnode.ExParams {
	return tunnelnode.ExParams{
		PublicAddress: p["public_address"], Port: port, Target: p["target"],
		RouteID: p["route_id"], RecordShaping: p["record_shaping"] == "true",
	}
}

func irParams(p map[string]string) tunnelnode.IRParams {
	return tunnelnode.IRParams{RouteListen: p["route_listen"], RouteID: p["route_id"]}
}
