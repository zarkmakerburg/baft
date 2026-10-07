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
	base := a.cfg.Tunnel
	if base == nil {
		return "", "", errors.New("tunnel changes are not enabled on this agent")
	}
	p, id := j.Params, j.Params["tunnel_id"]
	t := base
	if j.Action != agentjob.ActionTunnelDiscover {
		var err error
		t, err = base.ForInstance(p["instance_id"], p["metrics_listen"])
		if err != nil {
			return "", "", err
		}
	}
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
	case agentjob.ActionTunnelDiscover:
		b, err := json.Marshal(t.Discover(ctx))
		return string(b), "", err
	case agentjob.ActionTunnelFinalize:
		d, err := t.Finalize(ctx, id)
		return d, "", err
	case agentjob.ActionTunnelRollback:
		d, err := t.Rollback(ctx, id)
		return d, "", err
	case agentjob.ActionTunnelRetire:
		generation, _ := strconv.Atoi(p["generation"])
		ev, err := t.Retire(ctx, id, tunnelnode.RetireExpectation{
			Generation: generation, ConfigSHA256: p["config_sha256"],
			UnitSHA256: p["unit_sha256"], MarkerSHA256: p["marker_sha256"],
		})
		if err != nil {
			return "", "", err
		}
		b, err := json.Marshal(ev)
		return string(b), "", err
	}
	if strings.HasPrefix(j.Action, "tunnel_cert_") {
		return a.certRotation(ctx, t, j)
	}
	return "", "", fmt.Errorf("action %s has no tunnel handler", j.Action)
}

func exParams(p map[string]string, port int) tunnelnode.ExParams {
	return tunnelnode.ExParams{
		PublicAddress: p["public_address"], Port: port, Target: p["target"],
		RouteID: p["route_id"], RecordShaping: p["record_shaping"] == "true", MetricsListen: p["metrics_listen"],
	}
}

func irParams(p map[string]string) tunnelnode.IRParams {
	return tunnelnode.IRParams{RouteListen: p["route_listen"], RouteID: p["route_id"], MetricsListen: p["metrics_listen"]}
}

// certRotation runs one step of a certificate rotation. Every step answers
// with the node's own evidence; the EX's PREPARE also returns the public
// plan (new CA and leaf certificate) as output.
func (a *Agent) certRotation(ctx context.Context, t *tunnelnode.Manager, j agentjob.Job) (string, string, error) {
	p, rid := j.Params, j.Params["rotation_id"]
	if st, err := t.RotationState(rid); err == nil && st.TunnelID != p["tunnel_id"] {
		return "", "", fmt.Errorf("rotation %s belongs to tunnel %s", rid, st.TunnelID)
	}
	epoch, _ := strconv.Atoi(p["epoch"])
	switch j.Action {
	case agentjob.ActionCertPrepareEX:
		plan, err := t.RotatePrepareEX(ctx, rid, p["tunnel_id"], epoch)
		if err != nil {
			return "", "", err
		}
		b, err := json.Marshal(plan)
		return "new certificate staged for " + plan.Host, string(b), err
	case agentjob.ActionCertTrustIR:
		ca, err := tunnelnode.DecodeDER(p["ca_der"])
		if err != nil {
			return "", "", errors.New("ca_der is not base64url")
		}
		d, err := t.RotateTrustIR(ctx, rid, p["tunnel_id"], epoch, ca, p["ca_sha256"])
		return d, "", err
	case agentjob.ActionCertVerifyEX:
		d, err := t.RotateVerifyEX(ctx, rid)
		return d, "", err
	case agentjob.ActionCertVerifyIR:
		cert, err := tunnelnode.DecodeDER(p["cert_der"])
		if err != nil {
			return "", "", errors.New("cert_der is not base64url")
		}
		d, err := t.RotateVerifyIR(ctx, rid, cert, p["cert_sha256"])
		return d, "", err
	case agentjob.ActionCertActivateEX:
		d, err := t.RotateActivateEX(ctx, rid)
		return d, "", err
	case agentjob.ActionCertConfirmIR:
		d, err := t.RotateConfirmIR(ctx, rid, p["cert_sha256"])
		return d, "", err
	case agentjob.ActionCertRetireIR:
		d, err := t.RotateRetireIR(ctx, rid)
		return d, "", err
	case agentjob.ActionCertRetireEX:
		d, err := t.RotateRetireEX(ctx, rid)
		return d, "", err
	case agentjob.ActionCertRollback:
		d, err := t.RotateRollback(ctx, rid, p["ex_never_activated"] == "true")
		return d, "", err
	}
	return "", "", fmt.Errorf("action %s has no handler", j.Action)
}
