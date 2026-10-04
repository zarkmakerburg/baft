package installer_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Run as `curl ... | bash -s -- --plan`: the script arrives on stdin and the
// role question must be read from the controlling terminal, not from stdin.
const ptyDriver = `
import os, pty, select, sys, time
script = open(sys.argv[1], 'rb').read()
answers = sys.argv[2:]
pid, fd = pty.fork()
if pid == 0:
    r, w = os.pipe()
    if os.fork() == 0:
        os.close(r); os.write(w, script); os._exit(0)
    os.close(w); os.dup2(r, 0)
    os.execvp('bash', ['bash', '-s', '--', '--plan'])
out = b''
answered = 0
end = time.time() + 60
while time.time() < end:
    if select.select([fd], [], [], 0.2)[0]:
        try:
            d = os.read(fd, 4096)
        except OSError:
            break
        if not d:
            break
        out += d
        # Answer each prompt once, when it is the unfinished last line.
        asked = out.count(b'Choose 1 or 2: ') + out.count(b'Public IP or hostname')
        if answers and asked > answered and not out.endswith(b'\n'):
            os.write(fd, answers.pop(0).encode() + b'\n')
            answered += 1
sys.stdout.write(out.decode(errors='replace'))
`

func runPiped(t *testing.T, r *planRig, answers ...string) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", append([]string{"-c", ptyDriver, script}, answers...)...)
	env := make([]string, 0, len(r.env))
	for _, e := range r.env {
		if !strings.HasPrefix(e, "BAFT_NONINTERACTIVE=") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pty driver: %v\n%s", err, out)
	}
	return string(out)
}

func TestInstallerAsksRoleWhenPipedWithTerminal(t *testing.T) {
	r := newPlanRig(t)
	ir := runPiped(t, r, "2")
	if !strings.Contains(ir, "Where is this server?") || !strings.Contains(ir, "pair with the EX using the pairing code") {
		t.Fatalf("answer 2 did not run the IR plan:\n%s", ir)
	}
	ex := runPiped(t, r, "x", "1")
	if !strings.Contains(ex, "Please type 1 or 2.") || !strings.Contains(ex, "issue a one-time pairing code") {
		t.Fatalf("invalid then 1 did not run the EX plan:\n%s", ex)
	}
}

func TestInstallerWithoutRoleOrTerminalFails(t *testing.T) {
	r := newPlanRig(t)
	cmd := exec.Command("bash", filepath.Join("..", "..", "install.sh"), "--plan")
	cmd.Env = append(r.env, "BAFT_NONINTERACTIVE=0")
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // no controlling terminal
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "--role ex or --role ir") {
		t.Fatalf("want a clear error without a terminal, got err=%v\n%s", err, out)
	}
	cmd = exec.Command("bash", filepath.Join("..", "..", "install.sh"), "--plan")
	cmd.Env = append(r.env, "BAFT_NONINTERACTIVE=1")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "is required") {
		t.Fatalf("BAFT_NONINTERACTIVE=1 must not ask: err=%v\n%s", err, out)
	}
}
