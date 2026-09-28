package cluster

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/node"
)

const RequiredForeignNodes = 6

type runtimeRunner interface {
	Run(context.Context, config.Config) error
}

type runtimeFactory func() runtimeRunner

type Master struct {
	newRuntime runtimeFactory
}

func NewMaster() *Master {
	return &Master{newRuntime: func() runtimeRunner { return node.NewRuntime() }}
}

func ValidateMasterConfigs(cfgs []config.Config) error {
	if len(cfgs) != RequiredForeignNodes {
		return fmt.Errorf("master requires exactly %d foreign-node configs, got %d", RequiredForeignNodes, len(cfgs))
	}

	masterID := cfgs[0].Node.ID
	if masterID == "" {
		return errors.New("master node id is required")
	}

	peerAddresses := make(map[string]struct{}, len(cfgs))
	metricsAddresses := make(map[string]struct{}, len(cfgs))
	localListeners := make(map[string]struct{})

	for i, cfg := range cfgs {
		if err := config.Validate(cfg); err != nil {
			return fmt.Errorf("node config %d: %w", i+1, err)
		}
		if cfg.Node.Role != "dialer" {
			return fmt.Errorf("node config %d: master peers must use dialer role", i+1)
		}
		if cfg.Node.ID != masterID {
			return fmt.Errorf("node config %d: node.id %q differs from master id %q", i+1, cfg.Node.ID, masterID)
		}
		if cfg.Noise == nil {
			return fmt.Errorf("node config %d: Noise IK must be configured", i+1)
		}
		if cfg.Peer == nil {
			return fmt.Errorf("node config %d: peer is required", i+1)
		}
		if _, exists := peerAddresses[cfg.Peer.Address]; exists {
			return fmt.Errorf("node config %d: duplicate peer address %s", i+1, cfg.Peer.Address)
		}
		peerAddresses[cfg.Peer.Address] = struct{}{}

		if _, exists := metricsAddresses[cfg.Management.MetricsListen]; exists {
			return fmt.Errorf("node config %d: duplicate metrics listener %s", i+1, cfg.Management.MetricsListen)
		}
		metricsAddresses[cfg.Management.MetricsListen] = struct{}{}

		outbound := 0
		for _, route := range cfg.Routes {
			if route.Direction != "outbound" {
				continue
			}
			outbound++
			if _, exists := localListeners[route.Listen]; exists {
				return fmt.Errorf("node config %d: duplicate local route listener %s", i+1, route.Listen)
			}
			localListeners[route.Listen] = struct{}{}
		}
		if outbound == 0 {
			return fmt.Errorf("node config %d: at least one outbound route is required", i+1)
		}
	}
	return nil
}

// Run starts six independent dialer runtimes and fails the whole master set if
// any one runtime exits unexpectedly. The parent context remains authoritative
// for normal shutdown.
func (m *Master) Run(ctx context.Context, cfgs []config.Config) error {
	if err := ValidateMasterConfigs(cfgs); err != nil {
		return err
	}
	if m == nil || m.newRuntime == nil {
		return errors.New("master runtime factory is not configured")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(cfgs))
	var wg sync.WaitGroup
	for i := range cfgs {
		cfg := cfgs[i]
		runner := m.newRuntime()
		if runner == nil {
			cancel()
			wg.Wait()
			return fmt.Errorf("node config %d: runtime factory returned nil", i+1)
		}
		wg.Add(1)
		go func(index int, r runtimeRunner) {
			defer wg.Done()
			err := r.Run(runCtx, cfg)
			if runCtx.Err() != nil {
				return
			}
			if err == nil {
				err = errors.New("runtime stopped unexpectedly")
			}
			select {
			case errCh <- fmt.Errorf("foreign node %d (%s): %w", index+1, cfg.Peer.Address, err):
			case <-runCtx.Done():
			}
		}(i, runner)
	}

	select {
	case <-ctx.Done():
		cancel()
		wg.Wait()
		return nil
	case err := <-errCh:
		cancel()
		wg.Wait()
		return err
	}
}
