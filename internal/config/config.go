package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
)

const SchemaVersion = 1

type Config struct {
	SchemaVersion int        `json:"schema_version"`
	Noise         *Noise     `json:"noise,omitempty"`
	Node          Node       `json:"node"`
	Peer          *Peer      `json:"peer,omitempty"`
	Server        *Server    `json:"server,omitempty"`
	TLS           TLS        `json:"tls"`
	Transport     Transport  `json:"transport"`
	Limits        Limits     `json:"limits"`
	Recovery      Recovery   `json:"recovery"`
	Routes        []Route    `json:"routes"`
	Management    Management `json:"management"`
	Telemetry     Telemetry  `json:"telemetry,omitempty"`
	Logging       Logging    `json:"logging"`
}

type Node struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}
type Peer struct {
	Address         string `json:"address"`
	ServerName      string `json:"server_name"`
	AllowedIdentity string `json:"allowed_identity"`
}
type Server struct {
	Listen                string   `json:"listen"`
	ServerName            string   `json:"server_name"`
	AllowedPeerIdentities []string `json:"allowed_peer_identities"`
}
type TLS struct {
	MinVersion     string `json:"min_version"`
	CAFile         string `json:"ca_file"`
	CertFile       string `json:"cert_file"`
	KeyFile        string `json:"key_file"`
	SessionTickets bool   `json:"session_tickets"`
}
type Transport struct {
	Primary   string `json:"primary"`
	H3Enabled bool   `json:"h3_enabled"`
	Shards    int    `json:"shards"`
	Profile   string `json:"profile"`
}
type Limits struct {
	MaxFlows          int `json:"max_flows"`
	DataMemoryMiB     int `json:"data_memory_mib"`
	ReceiveInitialKiB int `json:"receive_initial_kib"`
	ReceiveMaxMiB     int `json:"receive_max_mib"`
	ReplayMaxMiB      int `json:"replay_max_mib"`
}
type Recovery struct {
	Enabled          bool   `json:"enabled"`
	RetentionSeconds int    `json:"retention_seconds"`
	Mode             string `json:"mode,omitempty"`
	Durable          bool   `json:"durable,omitempty"`
}
type Route struct {
	ID           string   `json:"id"`
	Listen       string   `json:"listen,omitempty"`
	RemoteRoute  string   `json:"remote_route,omitempty"`
	Direction    string   `json:"direction"`
	TrafficClass string   `json:"traffic_class,omitempty"`
	Target       string   `json:"target,omitempty"`
	AllowedPeers []string `json:"allowed_peers,omitempty"`
}
type Management struct {
	UnixSocket    string `json:"unix_socket"`
	MetricsListen string `json:"metrics_listen"`
}
type Telemetry struct {
	Enabled                   bool   `json:"enabled"`
	BCCURL                    string `json:"bcc_url,omitempty"`
	AgentTokenEnv             string `json:"agent_token_env,omitempty"`
	IntervalSeconds           int    `json:"interval_seconds,omitempty"`
	RouteProbeIntervalSeconds int    `json:"route_probe_interval_seconds,omitempty"`
	SpoolPath                 string `json:"spool_path,omitempty"`
	SpoolMaxPending           int    `json:"spool_max_pending,omitempty"`
}
type Logging struct {
	Level   string `json:"level"`
	Payload bool   `json:"payload"`
}

func DecodeJSON(r io.Reader) (Config, error) {
	var cfg Config
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("config must contain exactly one JSON document")
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Validate(c Config) error {
	if err := validateNoise(c); err != nil {
		return err
	}
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version: %d", c.SchemaVersion)
	}
	if c.Node.ID == "" || len(c.Node.ID) > 64 {
		return errors.New("node.id must be 1..64 bytes")
	}
	if c.Node.Role != "dialer" && c.Node.Role != "listener" {
		return errors.New("node.role must be dialer or listener")
	}
	if c.TLS.MinVersion != "1.3" {
		return errors.New("tls.min_version must be 1.3")
	}
	if c.TLS.SessionTickets {
		return errors.New("tls.session_tickets must be false in baseline")
	}
	if c.TLS.CAFile == "" || c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
		return errors.New("tls ca_file/cert_file/key_file are required")
	}
	if c.Transport.Primary != "h2" {
		return errors.New("transport.primary must be h2 in baseline")
	}
	if c.Transport.H3Enabled {
		return errors.New("transport.h3_enabled is unsupported before H2 gate passes")
	}
	if c.Transport.Shards < 1 || c.Transport.Shards > 8 {
		return errors.New("transport.shards must be between 1 and 8")
	}
	if c.Transport.Profile == "" {
		return errors.New("transport.profile is required")
	}
	if c.Limits.MaxFlows < 1 || c.Limits.MaxFlows > 65535 {
		return errors.New("limits.max_flows is out of supported range")
	}
	if c.Limits.DataMemoryMiB < 16 || c.Limits.DataMemoryMiB > 4096 {
		return errors.New("limits.data_memory_mib is unreasonable")
	}
	if c.Limits.ReceiveInitialKiB < 1 || c.Limits.ReceiveMaxMiB < 1 || c.Limits.ReplayMaxMiB < 1 {
		return errors.New("receive/replay limits must be positive")
	}
	poolMiB := c.Limits.DataMemoryMiB / 2
	if c.Limits.ReceiveMaxMiB > poolMiB || c.Limits.ReplayMaxMiB > c.Limits.DataMemoryMiB-poolMiB {
		return errors.New("per-flow receive/replay cap exceeds its non-borrowing global pool")
	}
	if c.Recovery.RetentionSeconds < 0 || c.Recovery.RetentionSeconds > 300 {
		return errors.New("recovery.retention_seconds is out of range")
	}
	if c.Recovery.Enabled {
		if c.Recovery.Mode!="" && c.Recovery.Mode!="same_process" { return errors.New("recovery.mode supports only same_process in BAFT 0.3") }
		if c.Recovery.Durable { return errors.New("durable/process-restart recovery is unsupported in step 5.7") }
		if c.Recovery.RetentionSeconds==0 { return errors.New("recovery.retention_seconds must be positive when recovery is enabled") }
	} else if c.Recovery.Durable {
		return errors.New("recovery.durable requires an unsupported process-restart mode")
	}
	if c.Logging.Payload {
		return errors.New("logging.payload must be false")
	}
	if c.Management.UnixSocket == "" {
		return errors.New("management.unix_socket is required")
	}
	if err := validateLoopbackListen(c.Management.MetricsListen, "management.metrics_listen"); err != nil {
		return err
	}
	if err := validateTelemetry(c.Telemetry); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(c.Routes))
	for i, r := range c.Routes {
		if r.ID == "" || len(r.ID) > 64 {
			return fmt.Errorf("routes[%d].id must be 1..64 bytes", i)
		}
		if _, ok := seen[r.ID]; ok {
			return fmt.Errorf("duplicate route id: %s", r.ID)
		}
		seen[r.ID] = struct{}{}
		if r.Direction != "outbound" && r.Direction != "inbound" {
			return fmt.Errorf("route %s: invalid direction", r.ID)
		}
		if r.Direction == "outbound" {
			if r.Listen == "" || r.RemoteRoute == "" || r.Target != "" {
				return fmt.Errorf("route %s: outbound route requires listen+remote_route and no target", r.ID)
			}
			if err := validateLoopbackListen(r.Listen, "route.listen"); err != nil {
				return fmt.Errorf("route %s: %w", r.ID, err)
			}
		} else {
			if r.Target == "" || r.Listen != "" || r.RemoteRoute != "" {
				return fmt.Errorf("route %s: inbound route requires target and no listen/remote_route", r.ID)
			}
			if err := validateFixedTarget(r.Target); err != nil {
				return fmt.Errorf("route %s: %w", r.ID, err)
			}
			if len(r.AllowedPeers) == 0 {
				return fmt.Errorf("route %s: allowed_peers must not be empty", r.ID)
			}
		}
	}
	switch c.Node.Role {
	case "dialer":
		if c.Peer == nil || c.Server != nil {
			return errors.New("dialer requires peer and must not define server")
		}
		if c.Peer.Address == "" || c.Peer.ServerName == "" || c.Peer.AllowedIdentity == "" {
			return errors.New("peer address/server_name/allowed_identity are required")
		}
		if _, _, err := net.SplitHostPort(c.Peer.Address); err != nil {
			return fmt.Errorf("peer.address: %w", err)
		}
	case "listener":
		if c.Server == nil || c.Peer != nil {
			return errors.New("listener requires server and must not define peer")
		}
		if c.Server.Listen == "" || c.Server.ServerName == "" || len(c.Server.AllowedPeerIdentities) == 0 {
			return errors.New("server listen/server_name/allowed_peer_identities are required")
		}
		if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
			return fmt.Errorf("server.listen: %w", err)
		}
	}
	return nil
}

func validateLoopbackListen(addr, field string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if port == "" || port == "0" {
		return fmt.Errorf("%s: explicit non-zero port required", field)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must use an explicit loopback IP", field)
	}
	return nil
}
func validateFixedTarget(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if port == "" || port == "0" {
		return errors.New("target requires explicit non-zero port")
	}
	if strings.ContainsAny(host, "*?") {
		return errors.New("target wildcard is forbidden")
	}
	if net.ParseIP(host) == nil {
		return errors.New("baseline target must be a fixed IP address")
	}
	return nil
}


func validateTelemetry(t Telemetry) error {
	if !t.Enabled { return nil }
	if t.BCCURL=="" { return errors.New("telemetry.bcc_url is required when enabled") }
	u,err:=url.Parse(t.BCCURL);if err!=nil||u.Host==""{return errors.New("telemetry.bcc_url must be an absolute URL")}
	if u.RawQuery!=""||u.Fragment!=""||u.User!=nil{return errors.New("telemetry.bcc_url must not contain credentials, query, or fragment")}
	if u.Path!=""&&u.Path!="/"{return errors.New("telemetry.bcc_url must not contain an API path")}
	if u.Scheme!="https"{
		host:=u.Hostname()
		ip:=net.ParseIP(host)
		if u.Scheme!="http"||!(host=="localhost"||(ip!=nil&&ip.IsLoopback())) {
			return errors.New("telemetry.bcc_url must use https except on loopback")
		}
	}
	if t.IntervalSeconds<0||t.IntervalSeconds>3600{return errors.New("telemetry.interval_seconds is out of range")}
	if t.RouteProbeIntervalSeconds<0||t.RouteProbeIntervalSeconds>60{return errors.New("telemetry.route_probe_interval_seconds is out of range")}
	if t.RouteProbeIntervalSeconds>0&&t.RouteProbeIntervalSeconds<1{return errors.New("telemetry.route_probe_interval_seconds is too small")}
	if strings.ContainsAny(t.AgentTokenEnv,"=\x00"){return errors.New("telemetry.agent_token_env is invalid")}
	if strings.ContainsRune(t.SpoolPath,'\x00'){return errors.New("telemetry.spool_path is invalid")}
	if t.SpoolMaxPending<0||t.SpoolMaxPending>1_000_000{return errors.New("telemetry.spool_max_pending is out of range")}
	return nil
}
