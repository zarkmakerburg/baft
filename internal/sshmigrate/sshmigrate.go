package sshmigrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const DropInName = "99-baft-port-migration.conf"

var portLine = regexp.MustCompile(`(?mi)^\s*Port\s+[0-9]+\s*(?:#.*)?$`)

type System interface {
	Run(context.Context, string, ...string) (string, error)
	Systemctl(context.Context, ...string) (string, error)
}
type Config struct{ MainConfig, DropInDir, SSHD, Service string }

func DefaultConfig() Config {
	return Config{"/etc/ssh/sshd_config", "/etc/ssh/sshd_config.d", "sshd", "ssh"}
}
func ValidatePort(p int) error {
	if p < 1 || p > 65535 {
		return errors.New("invalid SSH port")
	}
	return nil
}
func dropPath(c Config) string { return filepath.Join(c.DropInDir, DropInName) }

func Preflight(c Config, newPort int) error {
	if err := ValidatePort(newPort); err != nil {
		return err
	}
	b, err := os.ReadFile(c.MainConfig)
	if err != nil {
		return fmt.Errorf("read sshd config: %w", err)
	}
	clean := bytes.Join(bytes.FieldsFunc(b, func(r rune) bool { return r == '\n' || r == '\r' }), []byte("\n"))
	if portLine.Match(clean) {
		return errors.New("unmanaged explicit Port directive exists; refusing automatic migration")
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(newPort))
	if err != nil {
		return fmt.Errorf("new SSH port is occupied or cannot bind: %w", err)
	}
	_ = ln.Close()
	if _, err := os.Stat(dropPath(c)); err == nil {
		return errors.New("BAFT SSH migration drop-in already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func write(c Config, ports ...int) error {
	if err := os.MkdirAll(c.DropInDir, 0755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# managed by BAFT safe SSH migration\n")
	for _, p := range ports {
		if err := ValidatePort(p); err != nil {
			return err
		}
		b.WriteString("Port " + strconv.Itoa(p) + "\n")
	}
	tmp := dropPath(c) + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, dropPath(c))
}
func validateReload(ctx context.Context, c Config, sys System) error {
	if out, err := sys.Run(ctx, c.SSHD, "-t"); err != nil {
		return fmt.Errorf("sshd -t failed: %v: %s", err, out)
	}
	if out, err := sys.Systemctl(ctx, "reload", c.Service); err != nil {
		return fmt.Errorf("reload sshd: %v: %s", err, out)
	}
	return nil
}
func Stage(ctx context.Context, c Config, sys System, oldPort, newPort int) error {
	if err := Preflight(c, newPort); err != nil {
		return err
	}
	if err := write(c, oldPort, newPort); err != nil {
		return err
	}
	if err := validateReload(ctx, c, sys); err != nil {
		_ = os.Remove(dropPath(c))
		_, _ = sys.Systemctl(ctx, "reload", c.Service)
		return err
	}
	return nil
}
func Commit(ctx context.Context, c Config, sys System, newPort int) error {
	if _, err := os.Stat(dropPath(c)); err != nil {
		return errors.New("migration is not staged")
	}
	old, err := os.ReadFile(dropPath(c))
	if err != nil {
		return err
	}
	if err := write(c, newPort); err != nil {
		return err
	}
	if err := validateReload(ctx, c, sys); err != nil {
		_ = os.WriteFile(dropPath(c), old, 0644)
		_, _ = sys.Systemctl(ctx, "reload", c.Service)
		return err
	}
	return nil
}
func Rollback(ctx context.Context, c Config, sys System) error {
	if err := os.Remove(dropPath(c)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := sys.Systemctl(ctx, "reload", c.Service)
	return err
}
