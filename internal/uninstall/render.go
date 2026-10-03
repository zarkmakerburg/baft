package uninstall

import (
	"path/filepath"
	"strings"
)

// The installer's units, rendered exactly as install.sh renders them
// (render_service_unit, render_agent_unit). A unit file that is byte-for-byte
// what the installer writes for the parameters it names is BAFT's own; a test
// runs the shell functions and compares. Both templates are unchanged since
// v0.1.0 (service) and v0.1.1 (agent).

type serviceParams struct {
	User, Bin, Config, StateDir string
	Caps                        bool
}

func renderServiceUnit(p serviceParams) string {
	caps := ""
	if p.Caps {
		caps = "AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE"
	}
	return `[Unit]
Description=BAFT transport service
After=network-online.target
Wants=network-online.target
# The IR dialer exits while its EX is unreachable; keep retrying forever.
StartLimitIntervalSec=0

[Service]
Type=simple
User=` + p.User + `
Group=` + p.User + `
ExecStart=` + p.Bin + ` run --file ` + p.Config + `
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=2s
` + caps + `
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
ReadWritePaths=` + p.StateDir + `
UMask=0027

[Install]
WantedBy=multi-user.target
`
}

type agentParams struct {
	AgentBin, BCCURL, NodeID, AgentDir  string
	ReleaseRoot                         bool
	AgentStateDir, ReleaseState, BinDir string
	Service, ConfigDir, BaftStateDir    string
	User, MetricsListen, Interval       string
	AllowHTTP                           bool
	ReleaseBaseURL, RevocationsURL      string
	Prefix, SystemdDir                  string
}

func renderAgentUnit(p agentParams) string {
	rootFlag := ""
	if p.ReleaseRoot {
		rootFlag = "--release-root " + p.AgentDir + "/release-root.pub"
	}
	httpFlag := ""
	if p.AllowHTTP {
		httpFlag = "--allow-insecure-http"
	}
	if p.ReleaseBaseURL != "" {
		httpFlag += " --release-base-url " + p.ReleaseBaseURL
	}
	if p.RevocationsURL != "" {
		httpFlag += " --revocations-url " + p.RevocationsURL
	}
	return `[Unit]
Description=BAFT agent (signed BCC jobs)
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=` + p.AgentBin + ` --bcc-url ` + p.BCCURL + ` --node-id ` + p.NodeID + ` --token-file ` + p.AgentDir + `/token --bcc-job-key ` + p.AgentDir + `/bcc-job.pub ` + rootFlag + ` --state-dir ` + p.AgentStateDir + ` --release-state ` + p.ReleaseState + ` --bin-dir ` + p.BinDir + ` --service ` + p.Service + ` --config-dir ` + p.ConfigDir + ` --baft-state-dir ` + p.BaftStateDir + ` --service-user ` + p.User + ` --metrics-listen ` + p.MetricsListen + ` --interval ` + p.Interval + ` ` + httpFlag + `
Restart=always
RestartSec=10s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
# Tunnel jobs write the BAFT config, keys and unit.
ReadWritePaths=` + p.BinDir + ` ` + p.Prefix + ` ` + p.AgentStateDir + ` ` + p.ConfigDir + ` ` + p.BaftStateDir + ` ` + p.SystemdDir + `
UMask=0077

[Install]
WantedBy=multi-user.target
`
}

// unitLines returns the value of the first `key=` line and whether the file
// has a line exactly equal to each of the given lines.
func firstValue(unit, key string) string {
	for _, l := range strings.Split(unit, "\n") {
		if strings.HasPrefix(l, key+"=") {
			return strings.TrimPrefix(l, key+"=")
		}
	}
	return ""
}

func hasLine(unit, line string) bool {
	for _, l := range strings.Split(unit, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

// installerServiceParams reads the installer parameters a transport unit
// names; ok is false when it cannot be the installer's unit at all.
func installerServiceParams(unit string) (serviceParams, bool) {
	f := strings.Fields(firstValue(unit, "ExecStart"))
	if len(f) != 4 || f[1] != "run" || f[2] != "--file" {
		return serviceParams{}, false
	}
	p := serviceParams{
		User: firstValue(unit, "User"), Bin: f[0], Config: f[3],
		StateDir: firstValue(unit, "ReadWritePaths"),
		Caps:     hasLine(unit, "AmbientCapabilities=CAP_NET_BIND_SERVICE"),
	}
	return p, p.User != "" && p.StateDir != ""
}

// installerAgentParams reads the installer parameters an agent unit names.
func installerAgentParams(unit string) (agentParams, bool) {
	f := strings.Fields(firstValue(unit, "ExecStart"))
	if len(f) < 2 || filepath.Base(f[0]) != "baft-agent" {
		return agentParams{}, false
	}
	p := agentParams{AgentBin: f[0]}
	val := map[string]string{}
	for i := 1; i < len(f); i++ {
		switch f[i] {
		case "--allow-insecure-http":
			p.AllowHTTP = true
		default:
			if !strings.HasPrefix(f[i], "--") || i+1 >= len(f) {
				return agentParams{}, false
			}
			if _, dup := val[f[i]]; dup {
				return agentParams{}, false
			}
			val[f[i]] = f[i+1]
			i++
		}
	}
	tok := val["--token-file"]
	p.AgentDir = filepath.Dir(tok)
	if tok == "" || filepath.Base(tok) != "token" || val["--bcc-job-key"] != p.AgentDir+"/bcc-job.pub" {
		return agentParams{}, false
	}
	if rr, ok := val["--release-root"]; ok {
		if rr != p.AgentDir+"/release-root.pub" {
			return agentParams{}, false
		}
		p.ReleaseRoot = true
	}
	p.BCCURL, p.NodeID = val["--bcc-url"], val["--node-id"]
	p.AgentStateDir, p.ReleaseState, p.BinDir = val["--state-dir"], val["--release-state"], val["--bin-dir"]
	p.Service, p.ConfigDir, p.BaftStateDir = val["--service"], val["--config-dir"], val["--baft-state-dir"]
	p.User, p.MetricsListen, p.Interval = val["--service-user"], val["--metrics-listen"], val["--interval"]
	p.ReleaseBaseURL, p.RevocationsURL = val["--release-base-url"], val["--revocations-url"]
	rw := strings.Fields(firstValue(unit, "ReadWritePaths"))
	if len(rw) != 6 {
		return agentParams{}, false
	}
	p.Prefix, p.SystemdDir = rw[1], rw[5]
	return p, true
}

// InstallerServiceUnit is the transport unit install.sh writes (for tests and
// tools that need to recognise it).
func InstallerServiceUnit(user, bin, config, stateDir string, privilegedPort bool) string {
	return renderServiceUnit(serviceParams{User: user, Bin: bin, Config: config, StateDir: stateDir, Caps: privilegedPort})
}
