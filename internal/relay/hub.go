// Package relay implements the central WebSocket hub that routes messages
// between host agents and browser clients.
package relay

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sven97/agentcockpit/internal/protocol"
	"github.com/sven97/agentcockpit/internal/store"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 30 * time.Second
	maxMessageSize = 512 * 1024 // 512 KB
)

// Hub routes messages between connected host agents and browser clients.
// All public methods are goroutine-safe.
type Hub struct {
	mu       sync.RWMutex
	hosts    map[string]*HostConn    // hostID → connection
	browsers map[string]*BrowserConn // connID → connection
	store    store.Store

	// Pending approval requests waiting for a browser response.
	// requestID → channel that receives the ApprovalResponse.
	pendingApprovals   map[string]chan *protocol.ApprovalResponse
	pendingApprovalsMu sync.Mutex
}

// NewHub creates a ready-to-use Hub.
func NewHub(st store.Store) *Hub {
	return &Hub{
		hosts:            make(map[string]*HostConn),
		browsers:         make(map[string]*BrowserConn),
		store:            st,
		pendingApprovals: make(map[string]chan *protocol.ApprovalResponse),
	}
}

// ── Host connections ──────────────────────────────────────────────────────────

// HostConn represents a connected host agent.
type HostConn struct {
	id     string // hostID from DB
	userID string
	conn   *websocket.Conn
	send   chan []byte
	hub    *Hub
}

// RegisterHost adds a host connection to the hub.
func (h *Hub) RegisterHost(hostID, userID string, conn *websocket.Conn) *HostConn {
	hc := &HostConn{
		id:     hostID,
		userID: userID,
		conn:   conn,
		send:   make(chan []byte, 256),
		hub:    h,
	}
	h.mu.Lock()
	h.hosts[hostID] = hc
	h.mu.Unlock()
	return hc
}

// UnregisterHost removes a host connection and notifies browser clients.
func (h *Hub) UnregisterHost(hostID string) {
	h.mu.Lock()
	delete(h.hosts, hostID)
	h.mu.Unlock()
	h.broadcastToUserBrowsers(hostID, mustJSON(map[string]string{
		"type":   "host_status",
		"hostId": hostID,
		"status": "offline",
	}))
}

// SendToHost sends a JSON message to a specific host agent.
func (h *Hub) SendToHost(hostID string, msg any) bool {
	h.mu.RLock()
	hc, ok := h.hosts[hostID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	hc.send <- mustJSON(msg)
	return true
}

// RunHostWriter pumps outbound messages to the host WebSocket.
// Runs in its own goroutine per host.
func (hc *HostConn) RunWriter() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		hc.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-hc.send:
			hc.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				hc.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := hc.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			hc.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := hc.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// RunHostReader reads incoming messages from the host WebSocket and routes them.
// Runs in its own goroutine per host.
func (hc *HostConn) RunReader() {
	defer func() {
		hc.hub.UnregisterHost(hc.id)
		close(hc.send)
	}()
	hc.conn.SetReadLimit(maxMessageSize)
	hc.conn.SetReadDeadline(time.Now().Add(pongWait))
	hc.conn.SetPongHandler(func(string) error {
		hc.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		msgType, data, err := hc.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("host %s read error: %v", hc.id, err)
			}
			return
		}
		switch msgType {
		case websocket.BinaryMessage:
			hc.hub.routePTYOutput(hc, data)
		case websocket.TextMessage:
			hc.hub.routeHostMessage(hc, data)
		}
	}
}

// ── Browser connections ───────────────────────────────────────────────────────

// BrowserConn represents a connected browser tab.
type BrowserConn struct {
	id     string // random conn ID
	userID string
	conn   *websocket.Conn
	send   chan []byte
	hub    *Hub
}

// RegisterBrowser adds a browser connection to the hub.
func (h *Hub) RegisterBrowser(connID, userID string, conn *websocket.Conn) *BrowserConn {
	bc := &BrowserConn{
		id:     connID,
		userID: userID,
		conn:   conn,
		send:   make(chan []byte, 256),
		hub:    h,
	}
	h.mu.Lock()
	h.browsers[connID] = bc
	h.mu.Unlock()
	return bc
}

// UnregisterBrowser removes a browser connection.
func (h *Hub) UnregisterBrowser(connID string) {
	h.mu.Lock()
	delete(h.browsers, connID)
	h.mu.Unlock()
}

// RunBrowserWriter pumps outbound messages to the browser WebSocket.
func (bc *BrowserConn) RunWriter() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		bc.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-bc.send:
			bc.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				bc.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := bc.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			bc.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := bc.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// RunBrowserReader reads messages from the browser (e.g. approval decisions).
func (bc *BrowserConn) RunReader() {
	defer func() {
		bc.hub.UnregisterBrowser(bc.id)
		close(bc.send)
	}()
	bc.conn.SetReadLimit(64 * 1024)
	bc.conn.SetReadDeadline(time.Now().Add(pongWait))
	bc.conn.SetPongHandler(func(string) error {
		bc.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, data, err := bc.conn.ReadMessage()
		if err != nil {
			return
		}
		bc.hub.routeBrowserMessage(bc, data)
	}
}

// ── Message routing ───────────────────────────────────────────────────────────

// routePTYOutput fans out binary PTY output frames to all browsers watching
// the session. Frame format: [0x01][16-byte sessionId][data].
func (h *Hub) routePTYOutput(hc *HostConn, data []byte) {
	if len(data) < 17 || data[0] != protocol.FramePTY {
		return
	}
	sessionID := string(data[1:17])
	h.broadcastToUserBrowsers(hc.id, data) // forward raw frame; browser decodes sessionID
	_ = sessionID
}

// routeHostMessage handles JSON control messages from a host agent.
func (h *Hub) routeHostMessage(hc *HostConn, data []byte) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return
	}

	switch envelope.Type {
	case protocol.TypeApprovalRequest:
		var msg protocol.ApprovalRequest
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		h.handleApprovalRequest(hc, &msg)

	case protocol.TypeSessionStarted, protocol.TypeSessionStopped:
		// Forward status updates to all browser clients of this user.
		h.broadcastToUserBrowsers(hc.id, data)
	}
}

// routeBrowserMessage handles JSON messages from a browser client.
func (h *Hub) routeBrowserMessage(bc *BrowserConn, data []byte) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return
	}

	switch envelope.Type {
	case protocol.TypeApprovalResponse:
		var msg protocol.ApprovalResponse
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		h.handleApprovalResponse(bc, &msg)
	}
}

// ── Approval flow ─────────────────────────────────────────────────────────────

// handleApprovalRequest receives an approval request from a host, persists it,
// and pushes it to all browser clients of the owning user.
func (h *Hub) handleApprovalRequest(hc *HostConn, msg *protocol.ApprovalRequest) {
	// Register a channel to receive the eventual response.
	ch := make(chan *protocol.ApprovalResponse, 1)
	h.pendingApprovalsMu.Lock()
	h.pendingApprovals[msg.RequestID] = ch
	h.pendingApprovalsMu.Unlock()

	// Push to all browser clients for this user.
	h.broadcastToUserBrowsers(hc.id, mustJSON(msg))
}

// handleApprovalResponse receives an approval decision from a browser client,
// routes it to the host agent, and unblocks the waiting hook shim.
func (h *Hub) handleApprovalResponse(bc *BrowserConn, msg *protocol.ApprovalResponse) {
	h.pendingApprovalsMu.Lock()
	ch, ok := h.pendingApprovals[msg.RequestID]
	if ok {
		delete(h.pendingApprovals, msg.RequestID)
	}
	h.pendingApprovalsMu.Unlock()

	if ok {
		ch <- msg
	}
}

// WaitForApproval blocks until a browser client sends a decision for requestID.
// The host's RunHostReader goroutine calls this after registering the pending channel.
// Returns the decision or blocks indefinitely (no timeout by design).
func (h *Hub) WaitForApproval(requestID string) *protocol.ApprovalResponse {
	h.pendingApprovalsMu.Lock()
	ch, ok := h.pendingApprovals[requestID]
	h.pendingApprovalsMu.Unlock()
	if !ok {
		return nil
	}
	return <-ch
}

// DeliverApprovalToHost sends the approval decision back to the host agent.
func (h *Hub) DeliverApprovalToHost(hostID string, resp *protocol.ApprovalResponse) {
	h.SendToHost(hostID, resp)
}

// ── Broadcast helpers ─────────────────────────────────────────────────────────

// broadcastToUserBrowsers sends a message to all browser connections whose
// userID matches the owner of the given hostID.
func (h *Hub) broadcastToUserBrowsers(hostID string, data []byte) {
	h.mu.RLock()
	hc, ok := h.hosts[hostID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	userID := hc.userID
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, bc := range h.browsers {
		if bc.userID == userID {
			select {
			case bc.send <- data:
			default:
				// Browser is slow; drop the frame rather than block.
			}
		}
	}
}

// BroadcastToUser sends a JSON message directly to all browsers of a user.
// Used by the HTTP API to push events (e.g. host status changes).
func (h *Hub) BroadcastToUser(userID string, msg any) {
	data := mustJSON(msg)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, bc := range h.browsers {
		if bc.userID == userID {
			select {
			case bc.send <- data:
			default:
			}
		}
	}
}

// ── Graceful shutdown ─────────────────────────────────────────────────────────

// Shutdown sends a server_shutdown notice to all connected hosts and browsers,
// then closes all connections.
func (h *Hub) Shutdown(reconnectIn int) {
	msg := mustJSON(protocol.ServerShutdown{
		Type:        protocol.TypeServerShutdown,
		ReconnectIn: reconnectIn,
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, hc := range h.hosts {
		select {
		case hc.send <- msg:
		default:
		}
		close(hc.send)
	}
	for _, bc := range h.browsers {
		select {
		case bc.send <- msg:
		default:
		}
		close(bc.send)
	}
}

// ── Utilities ─────────────────────────────────────────────────────────────────

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
