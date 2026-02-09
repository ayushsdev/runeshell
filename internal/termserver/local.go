package termserver

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"
)

var execCommand = exec.Command
var ptyStart = pty.Start

type LocalSessionManager struct {
	Shell string
	Tmux  string
	// KillOnClose terminates the tmux session when the PTY is closed.
	KillOnClose bool
}

type SessionLister interface {
	ListSessions() ([]string, error)
}

func (m *LocalSessionManager) Attach(sessionID string) (Session, error) {
	shell := m.Shell
	if shell == "" {
		shell = "bash"
	}
	tmux := m.Tmux
	if tmux == "" {
		tmux = "tmux"
	}

	cmd := execCommand(shell, "-lc", fmt.Sprintf("%s new -As %s", tmux, sessionID))
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := ptyStart(cmd)
	if err != nil {
		return nil, err
	}

	session := &PTYSession{
		cmd:         cmd,
		pty:         ptmx,
		output:      make(chan []byte, 32),
		sessionID:   sessionID,
		killOnClose: m.KillOnClose,
		tmux:        tmux,
	}
	go session.readLoop()
	go session.wait()
	return session, nil
}

func (m *LocalSessionManager) ListSessions() ([]string, error) {
	shell := m.Shell
	if shell == "" {
		shell = "bash"
	}
	tmux := m.Tmux
	if tmux == "" {
		tmux = "tmux"
	}

	cmd := execCommand(shell, "-lc", fmt.Sprintf("%s list-sessions -F '#S'", tmux))
	out, err := cmd.Output()
	if err != nil {
		return []string{}, nil
	}
	lines := strings.Fields(string(out))
	return lines, nil
}

type PTYSession struct {
	cmd         *exec.Cmd
	pty         *os.File
	output      chan []byte
	sessionID   string
	killOnClose bool
	tmux        string
	close       sync.Once
}

func (s *PTYSession) Write(p []byte) error {
	_, err := s.pty.Write(p)
	return err
}

func (s *PTYSession) Resize(cols, rows int) error {
	return pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (s *PTYSession) Output() <-chan []byte {
	return s.output
}

func (s *PTYSession) Close() error {
	s.close.Do(func() {
		_ = s.pty.Close()
		if s.killOnClose && s.sessionID != "" && s.tmux != "" {
			_ = execCommand(s.tmux, "kill-session", "-t", s.sessionID).Run()
		}
	})
	return nil
}

func (s *PTYSession) readLoop() {
	defer close(s.output)
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.output <- chunk
		}
		if err != nil {
			if err == io.EOF {
				return
			}
			return
		}
	}
}

func (s *PTYSession) wait() {
	_ = s.cmd.Wait()
	_ = s.Close()
}
