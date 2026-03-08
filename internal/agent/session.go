package agent

import (
	"encoding/binary"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/sven97/agentcockpit/internal/protocol"
)

// sessionPool manages the active PTY sessions on this host.
type sessionPool struct {
	mu       sync.RWMutex
	sessions map[string]*session
}

func newSessionPool() *sessionPool {
	return &sessionPool{sessions: make(map[string]*session)}
}

// session represents a single running PTY process.
type session struct {
	id      string
	ptmx    *os.File
	cmd     *exec.Cmd
	seq     uint64
	seqMu   sync.Mutex
	sendFn  func(any)
}

// shellPath returns the best available shell, trying SHELL env var and common
// absolute paths so it works even when PATH is minimal (e.g. under launchd).
func shellPath() string {
	if s := os.Getenv("SHELL"); s != "" {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	for _, s := range []string{"/bin/bash", "/bin/sh", "/usr/bin/bash", "/usr/bin/sh"} {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return "sh" // last resort — rely on PATH
}

// start spawns a new PTY session and streams its output to the relay.
func (p *sessionPool) start(msg protocol.SessionCreate, sendFn func(any)) {
	cmd := exec.Command(shellPath(), "-c", msg.Command)
	cmd.Dir = msg.CWD
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"AGENTCOCKPIT_SESSION_ID="+msg.SessionID,
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("pty start session %s: %v", msg.SessionID, err)
		sendFn(protocol.SessionStopped{
			Type:      protocol.TypeSessionStopped,
			SessionID: msg.SessionID,
			ExitCode:  -1,
		})
		return
	}

	sess := &session{
		id:     msg.SessionID,
		ptmx:   ptmx,
		cmd:    cmd,
		sendFn: sendFn,
	}

	p.mu.Lock()
	p.sessions[msg.SessionID] = sess
	p.mu.Unlock()

	sendFn(protocol.SessionStarted{
		Type:      protocol.TypeSessionStarted,
		SessionID: msg.SessionID,
		AgentType: msg.AgentType,
		CWD:       msg.CWD,
		PID:       cmd.Process.Pid,
	})

	// Stream PTY output to the relay.
	go sess.streamOutput()

	// Wait for process exit.
	go func() {
		err := cmd.Wait()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
		ptmx.Close()
		p.mu.Lock()
		delete(p.sessions, msg.SessionID)
		p.mu.Unlock()
		sendFn(protocol.SessionStopped{
			Type:      protocol.TypeSessionStopped,
			SessionID: msg.SessionID,
			ExitCode:  exitCode,
		})
	}()
}

// streamOutput reads PTY output and sends binary frames to the relay.
// Frame format: [0x01][32-byte sessionId ASCII hex][data] = 33-byte header.
func (s *session) streamOutput() {
	buf := make([]byte, 32*1024)
	// Pre-build the 33-byte frame header.
	header := make([]byte, 33)
	header[0] = protocol.FramePTY
	sid := []byte(s.id)
	copy(header[1:33], sid) // session IDs are exactly 32 hex chars

	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			frame := make([]byte, 33+n)
			copy(frame, header)
			copy(frame[33:], buf[:n])
			s.sendFn(rawFrame(frame))
		}
		if err != nil {
			return
		}
	}
}

// rawFrame is a sentinel type so sendFn can detect binary vs JSON payloads.
type rawFrame []byte

// kill sends SIGKILL to a session's process.
func (p *sessionPool) kill(sessionID string) {
	p.mu.RLock()
	sess, ok := p.sessions[sessionID]
	p.mu.RUnlock()
	if ok && sess.cmd.Process != nil {
		sess.cmd.Process.Kill()
	}
}

// writeStdin writes data to a session's PTY stdin.
func (p *sessionPool) writeStdin(sessionID string, data []byte) {
	p.mu.RLock()
	sess, ok := p.sessions[sessionID]
	p.mu.RUnlock()
	if ok {
		sess.ptmx.Write(data)
	}
}

// nextSeq returns a monotonically increasing sequence number for a session.
func (s *session) nextSeq() uint64 {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.seq++
	return s.seq
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Ensure binary is imported (used for future framing extensions).
var _ = binary.BigEndian

// flushTime is how long to wait before flushing buffered output to the DB.
// The live streaming path (WebSocket fan-out) is immediate; DB writes are batched.
const flushTime = 100 * time.Millisecond
