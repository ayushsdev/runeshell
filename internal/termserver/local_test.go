package termserver

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestLocalSessionManagerListSessionsParsesOutput(t *testing.T) {
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	var gotName string
	var gotArgs []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string{}, args...)
		return exec.Command("sh", "-c", "printf 'ai\\ndev\\n'")
	}

	mgr := &LocalSessionManager{}
	sessions, err := mgr.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if !reflect.DeepEqual(sessions, []string{"ai", "dev"}) {
		t.Fatalf("unexpected sessions: %+v", sessions)
	}
	if gotName != "bash" {
		t.Fatalf("expected default shell bash, got %q", gotName)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "-lc" {
		t.Fatalf("unexpected command args: %+v", gotArgs)
	}
	if !strings.Contains(gotArgs[1], "tmux list-sessions -F '#S'") {
		t.Fatalf("unexpected tmux command: %q", gotArgs[1])
	}
}

func TestLocalSessionManagerAttachUsesDefaultsAndKillOnClose(t *testing.T) {
	origExec := execCommand
	origPtyStart := ptyStart
	t.Cleanup(func() {
		execCommand = origExec
		ptyStart = origPtyStart
	})

	var calls [][]string
	execCommand = func(name string, args ...string) *exec.Cmd {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		switch name {
		case "bash":
			return exec.Command("sh", "-c", "sleep 0.05")
		case "tmux":
			return exec.Command("sh", "-c", "exit 0")
		default:
			return exec.Command("sh", "-c", "exit 0")
		}
	}
	ptyStart = func(cmd *exec.Cmd) (*os.File, error) {
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return os.CreateTemp(t.TempDir(), "pty-*")
	}

	mgr := &LocalSessionManager{KillOnClose: true}
	sess, err := mgr.Attach("ai")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(calls) == 0 {
		t.Fatalf("expected command calls")
	}
	if calls[0][0] != "bash" || calls[0][1] != "-lc" || !strings.Contains(calls[0][2], "tmux new -As ai") {
		t.Fatalf("unexpected attach command: %+v", calls[0])
	}

	foundKill := false
	for _, c := range calls {
		if len(c) >= 4 && c[0] == "tmux" && c[1] == "kill-session" && c[2] == "-t" && c[3] == "ai" {
			foundKill = true
			break
		}
	}
	if !foundKill {
		t.Fatalf("expected tmux kill-session call, got %+v", calls)
	}
}

func TestLocalSessionManagerListSessionsCommandErrorReturnsEmpty(t *testing.T) {
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "exit 1")
	}

	mgr := &LocalSessionManager{}
	sessions, err := mgr.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected empty sessions on command error, got %+v", sessions)
	}
}
