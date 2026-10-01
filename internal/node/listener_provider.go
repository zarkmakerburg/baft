package node

import (
	"fmt"
	"net"
)

// EndpointKind identifies a Runtime-owned network endpoint. It is deliberately
// topology-agnostic: callers may provide any number of Runtime instances and
// routes. The production path does not require a ListenerProvider.
type EndpointKind string

const (
	EndpointServer  EndpointKind = "server"
	EndpointRoute   EndpointKind = "route"
	EndpointMetrics EndpointKind = "metrics"
)

// ListenerProvider is a test-only ownership seam. A fixture may bind a socket
// once, retain ownership until Runtime asks for it, then hand that exact
// listener to Runtime. Production leaves the provider nil and binds normally.
type ListenerProvider interface {
	Listener(kind EndpointKind, name, configuredAddress string) (net.Listener, error)
}

func (r *Runtime) SetListenerProviderForTest(p ListenerProvider) {
	r.listenerProviderMu.Lock()
	r.listenerProviderForTest = p
	r.listenerProviderMu.Unlock()
}

func (r *Runtime) takeEndpointListenerForTest(kind EndpointKind, name, configured string) (net.Listener, error) {
	r.listenerProviderMu.Lock()
	p := r.listenerProviderForTest
	r.listenerProviderMu.Unlock()
	if p == nil {
		return nil, nil
	}
	ln, err := p.Listener(kind, name, configured)
	if err != nil {
		return nil, err
	}
	if ln == nil {
		return nil, nil
	}
	if got := ln.Addr().String(); got != configured {
		_ = ln.Close()
		return nil, fmt.Errorf("test %s listener address mismatch got=%s want=%s", kind, got, configured)
	}
	return ln, nil
}
