package agent

import (
	"encoding/base64"
	"log"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	e2e "github.com/sven97/agentcockpit/internal/crypto"
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
	id         string
	ptmx       *os.File
	cmd        *exec.Cmd
	sendFn     func(any)
	sessionKey []byte // 32-byte AES-256-GCM key; nil when E2E is not active
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

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path string) string {
	if path == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
	} else if len(path) > 1 && path[:2] == "~/" {
		if h, err := os.UserHomeDir(); err == nil {
			return h + path[1:]
		}
	}
	return path
}

// start spawns a new PTY session and streams its output to the relay.
// If msg.UserE2EPubKey is set, an ephemeral keypair is generated and a shared
// AES-256-GCM session key is derived; all PTY output and stdin is then encrypted.
func (p *sessionPool) start(msg protocol.SessionCreate, sendFn func(any)) {
	shell := shellPath()
	var cmd *exec.Cmd
	if msg.Command == "" || msg.Command == "shell" {
		// Run an interactive login shell directly (no -c wrapper).
		cmd = exec.Command(shell, "-l")
	} else {
		cmd = exec.Command(shell, "-c", msg.Command)
	}
	cmd.Dir = expandHome(msg.CWD)
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

	// E2E: derive session key if the user has registered a long-term public key.
	var agentEphemeralPubKeyB64 string
	if msg.UserE2EPubKey != "" {
		userPubDER, err := base64.StdEncoding.DecodeString(msg.UserE2EPubKey)
		if err != nil {
			log.Printf("session %s: decode user E2E pubkey: %v — running unencrypted", msg.SessionID, err)
		} else {
			ephPriv, err := e2e.GenerateEphemeralKeypair()
			if err != nil {
				log.Printf("session %s: generate ephemeral keypair: %v — running unencrypted", msg.SessionID, err)
			} else {
				key, err := e2e.DeriveSessionKey(ephPriv, userPubDER, msg.SessionID)
				if err != nil {
					log.Printf("session %s: derive session key: %v — running unencrypted", msg.SessionID, err)
				} else {
					sess.sessionKey = key
					spki, err := e2e.MarshalPublicKeySPKI(ephPriv.PublicKey())
					if err == nil {
						agentEphemeralPubKeyB64 = base64.StdEncoding.EncodeToString(spki)
					}
				}
			}
		}
	}

	p.mu.Lock()
	p.sessions[msg.SessionID] = sess
	p.mu.Unlock()

	sendFn(protocol.SessionStarted{
		Type:                 protocol.TypeSessionStarted,
		SessionID:            msg.SessionID,
		AgentType:            msg.AgentType,
		CWD:                  msg.CWD,
		PID:                  cmd.Process.Pid,
		AgentEphemeralPubKey: agentEphemeralPubKeyB64,
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
		// Zero the session key before releasing.
		e2e.ZeroKey(sess.sessionKey)
		sendFn(protocol.SessionStopped{
			Type:      protocol.TypeSessionStopped,
			SessionID: msg.SessionID,
			ExitCode:  exitCode,
		})
	}()
}

// streamOutput reads PTY output and sends binary frames to the relay.
// Frame format: [0x01][32-byte sessionId ASCII hex][payload] = 33-byte header.
// When E2E is active, payload = [12-byte IV][AES-GCM ciphertext+tag].
// When E2E is not active, payload = raw PTY bytes.
func (s *session) streamOutput() {
	buf := make([]byte, 32*1024)
	// Pre-build the 33-byte frame header.
	header := make([]byte, 33)
	header[0] = protocol.FramePTY
	copy(header[1:33], []byte(s.id)) // session IDs are exactly 32 hex chars

	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			payload := buf[:n]
			if s.sessionKey != nil {
				encrypted, encErr := e2e.Seal(s.sessionKey, payload, []byte(s.id))
				if encErr != nil {
					log.Printf("session %s: encrypt PTY output: %v", s.id, encErr)
					// On encryption failure, skip this chunk rather than send plaintext.
					if err != nil {
						return
					}
					continue
				}
				payload = encrypted
			}
			frame := make([]byte, 33+len(payload))
			copy(frame, header)
			copy(frame[33:], payload)
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
// When E2E is active, data is a [12-byte IV][AES-GCM ciphertext] blob from the browser;
// it is decrypted before writing to the PTY.
func (p *sessionPool) writeStdin(sessionID string, data []byte) {
	p.mu.RLock()
	sess, ok := p.sessions[sessionID]
	p.mu.RUnlock()
	if !ok {
		return
	}
	plaintext := data
	if sess.sessionKey != nil {
		decrypted, err := e2e.Open(sess.sessionKey, data, []byte(sessionID))
		if err != nil {
			log.Printf("session %s: decrypt stdin: %v", sessionID, err)
			return // drop the frame rather than write garbage to the PTY
		}
		plaintext = decrypted
	}
	sess.ptmx.Write(plaintext)
}

// resize updates the PTY window size for a session.
func (p *sessionPool) resize(sessionID string, cols, rows uint16) {
	p.mu.RLock()
	sess, ok := p.sessions[sessionID]
	p.mu.RUnlock()
	if ok {
		pty.Setsize(sess.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
	}
}
