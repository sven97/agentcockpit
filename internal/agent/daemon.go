// Package agent implements the host agent daemon that runs on user machines.
// It connects outbound to the relay server via WebSocket, manages a pool of
// PTY sessions, and listens on a Unix socket for hook shim calls.
package agent

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sven97/agentcockpit/internal/protocol"
)


const (
	reconnectMin  = 5 * time.Second
	reconnectMax  = 60 * time.Second
	reconnectMult = 2.0
)

// Config holds the agent daemon configuration stored in agent.json.
// RelayURL is the HTTP(S) base URL of the relay server (e.g. https://agentcockpit.io).
type Config struct {
	RelayURL     string `json:"relay_url"`
	Token        string `json:"token"`
	Name         string `json:"name"`
	AgentVersion string `json:"-"` // injected at build time
	ConfigPath   string `json:"-"` // path to agent.json, set by the agent command
}

// wsMsg carries a WebSocket frame with its message type.
type wsMsg struct {
	data   []byte
	binary bool
}

// Daemon manages the WebSocket connection to the relay and the local session pool.
type Daemon struct {
	cfg      Config
	sessions *sessionPool

	// wsConn is the current relay WebSocket connection (nil when disconnected).
	wsMu   sync.Mutex
	wsConn *websocket.Conn

	send   chan wsMsg    // outbound to relay
	cancel func()       // cancels the Run ctx when host_removed is received
}

// New creates a Daemon from config.
func New(cfg Config) *Daemon {
	return &Daemon{
		cfg:      cfg,
		sessions: newSessionPool(),
		send:     make(chan wsMsg, 256),
	}
}

// Run starts the daemon: connects to the relay, starts the Unix socket listener,
// and reconnects automatically on disconnect. Blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	ctx, d.cancel = context.WithCancel(ctx)

	delay := reconnectMin
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		connected, err := d.connect(ctx)
		if connected {
			// We had a live connection; reset backoff so we reconnect quickly.
			delay = reconnectMin
		}
		if err != nil && err != context.Canceled {
			log.Printf("relay disconnected: %v — reconnecting in %s", err, delay)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		// Exponential backoff (applied only after failed dials).
		if !connected {
			delay = time.Duration(float64(delay) * reconnectMult)
			if delay > reconnectMax {
				delay = reconnectMax
			}
		}
	}
}

// connect establishes a WebSocket connection to the relay and runs the read/write
// loops until the connection drops or ctx is cancelled. Returns (true, err) if
// the connection was established (even if it later dropped), (false, err) on dial failure.
func (d *Daemon) connect(ctx context.Context) (connected bool, err error) {
	wsURL := httpToWS(d.cfg.RelayURL)
	header := map[string][]string{
		"Authorization": {"Bearer " + d.cfg.Token},
	}
	conn, _, dialErr := websocket.DefaultDialer.DialContext(ctx, wsURL+"/ws/host", header)
	if dialErr != nil {
		return false, dialErr
	}
	conn.SetReadLimit(512 * 1024)

	d.wsMu.Lock()
	d.wsConn = conn
	d.wsMu.Unlock()

	defer func() {
		d.wsMu.Lock()
		d.wsConn = nil
		d.wsMu.Unlock()
		conn.Close()
	}()

	// Send host_hello.
	hello := protocol.HostHello{
		Type:         protocol.TypeHostHello,
		Name:         d.cfg.Name,
		Platform:     runtime.GOOS,
		AgentVersion: d.cfg.AgentVersion,
	}
	if err := conn.WriteJSON(hello); err != nil {
		return true, err
	}

	// Report active sessions so the server can restore their status after a
	// restart (which would have marked them as "error" via MarkStaleSessionsAsError).
	sessIDs := d.sessions.activeIDs()
	if err := conn.WriteJSON(protocol.SessionList{
		Type:     protocol.TypeSessionList,
		Sessions: sessIDs,
	}); err != nil {
		return true, err
	}

	log.Printf("connected to relay %s as %q (%d active sessions)", d.cfg.RelayURL, d.cfg.Name, len(sessIDs))

	errc := make(chan error, 2)
	go d.wsReader(ctx, conn, errc)
	go d.wsWriter(ctx, conn, errc)
	return true, <-errc
}

// httpToWS converts an HTTP(S) URL to its WebSocket equivalent.
func httpToWS(u string) string {
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u
}

// wsWriter pumps outbound messages from d.send to the WebSocket.
func (d *Daemon) wsWriter(ctx context.Context, conn *websocket.Conn, errc chan<- error) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			errc <- ctx.Err()
			return
		case msg := <-d.send:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			msgType := websocket.TextMessage
			if msg.binary {
				msgType = websocket.BinaryMessage
			}
			if err := conn.WriteMessage(msgType, msg.data); err != nil {
				errc <- err
				return
			}
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				errc <- err
				return
			}
		}
	}
}

// wsReader reads messages from the relay WebSocket and dispatches them.
func (d *Daemon) wsReader(ctx context.Context, conn *websocket.Conn, errc chan<- error) {
	// Set an initial read deadline so we detect dead connections even when no
	// data flows (e.g. Cloudflare holding a TCP connection open after Cloud Run
	// restarts). Reset on pong (from our own pings) and on any received message.
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			errc <- err
			return
		}
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		d.handleRelayMessage(data)
	}
}

// handleRelayMessage dispatches a JSON message received from the relay server.
func (d *Daemon) handleRelayMessage(data []byte) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return
	}

	switch envelope.Type {
	case protocol.TypeSessionCreate:
		var msg protocol.SessionCreate
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		go d.sessions.start(msg, d.sendToRelay)

	case protocol.TypeSessionKill:
		var msg protocol.SessionKill
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		d.sessions.kill(msg.SessionID)

	case protocol.TypeStdinData:
		var msg protocol.StdinData
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		d.sessions.writeStdin(msg.SessionID, msg.Data)

	case protocol.TypeSessionResize:
		var msg protocol.SessionResize
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		d.sessions.resize(msg.SessionID, msg.Cols, msg.Rows)

	case protocol.TypeServerShutdown:
		log.Printf("relay is shutting down, will reconnect shortly")

	case protocol.TypeHostRemoved:
		log.Printf("host was removed from dashboard — stopping agent and removing config")
		if d.cfg.ConfigPath != "" {
			if err := os.Remove(d.cfg.ConfigPath); err != nil && !os.IsNotExist(err) {
				log.Printf("warning: could not remove config %s: %v", d.cfg.ConfigPath, err)
			}
		}
		if d.cancel != nil {
			d.cancel()
		}
	}
}

// sendToRelay queues a message for delivery to the relay server.
// rawFrame values are sent as binary WebSocket frames; everything else as JSON text.
func (d *Daemon) sendToRelay(msg any) {
	var m wsMsg
	switch v := msg.(type) {
	case rawFrame:
		m = wsMsg{data: []byte(v), binary: true}
	default:
		b, err := json.Marshal(msg)
		if err != nil {
			return
		}
		m = wsMsg{data: b}
	}
	select {
	case d.send <- m:
	default:
		log.Printf("relay send buffer full, dropping message")
	}
}

