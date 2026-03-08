// Package relay implements the central WebSocket hub that routes messages
// between host agents and browser clients.
package relay

import (
	"context"
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

// ApprovalPersistFn is called by the hub when an approval_request arrives,
// so the caller (server) can persist it to the database.
type ApprovalPersistFn func(ctx context.Context, approval *protocol.ApprovalRequest, userID string)

// sessionTokenState holds the latest known token usage for a session.
type sessionTokenState struct {
	InputTokens       int
	ContextWindowSize int
	UserID            string
}

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

	// sessionTokens tracks the latest context window usage per session.
	sessionTokens   map[string]*sessionTokenState
	sessionTokensMu sync.RWMutex

	// onApproval is called when an approval_request arrives (for DB persistence).
	onApproval ApprovalPersistFn
}

// NewHub creates a ready-to-use Hub.
func NewHub(st store.Store) *Hub {
	return &Hub{
		hosts:            make(map[string]*HostConn),
		browsers:         make(map[string]*BrowserConn),
		store:            st,
		pendingApprovals: make(map[string]chan *protocol.ApprovalResponse),
		sessionTokens:    make(map[string]*sessionTokenState),
	}
}

// SetApprovalPersistFn registers a callback invoked when an approval_request arrives.
func (h *Hub) SetApprovalPersistFn(fn ApprovalPersistFn) { h.onApproval = fn }

// ── Host connections ──────────────────────────────────────────────────────────

// HostConn represents a connected host agent.
type HostConn struct {
	ID     string // hostID from DB
	UserID string
	conn   *websocket.Conn
	send   chan []byte
	hub    *Hub
}

// Conn returns the underlying WebSocket connection.
func (hc *HostConn) Conn() *websocket.Conn { return hc.conn }

// RegisterHost adds a host connection to the hub.
func (h *Hub) RegisterHost(hostID, userID string, conn *websocket.Conn) *HostConn {
	hc := &HostConn{
		ID:     hostID,
		UserID: userID,
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
	h.BroadcastToUser(h.hostUserID(hostID), mustJSON(map[string]string{
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
	select {
	case hc.send <- mustJSON(msg):
		return true
	default:
		return false
	}
}

// RunWriter pumps outbound messages to the host WebSocket.
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

// RunReader reads incoming messages from the host WebSocket and routes them.
func (hc *HostConn) RunReader() {
	defer func() {
		hc.hub.UnregisterHost(hc.ID)
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
				log.Printf("host %s read error: %v", hc.ID, err)
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
	ID     string // random conn ID
	UserID string
	conn   *websocket.Conn
	send   chan []byte
	hub    *Hub
}

// RegisterBrowser adds a browser connection to the hub.
func (h *Hub) RegisterBrowser(connID, userID string, conn *websocket.Conn) *BrowserConn {
	bc := &BrowserConn{
		ID:     connID,
		UserID: userID,
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

// RunWriter pumps outbound messages to the browser WebSocket.
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
			msgType := websocket.TextMessage
			if len(msg) > 0 && msg[0] == protocol.FramePTY {
				msgType = websocket.BinaryMessage
			}
			if err := bc.conn.WriteMessage(msgType, msg); err != nil {
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

// RunReader reads messages from the browser (approval decisions).
func (bc *BrowserConn) RunReader() {
	defer func() {
		bc.hub.UnregisterBrowser(bc.ID)
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
// the session. Frame: [0x01][32-byte sessionId ASCII hex][data] = 33-byte header.
func (h *Hub) routePTYOutput(hc *HostConn, data []byte) {
	if len(data) < 33 || data[0] != protocol.FramePTY {
		return
	}
	// Forward raw frame to all browser clients for this user.
	h.broadcastToUserBrowsersByUserID(hc.UserID, data)
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

	case protocol.TypeSessionStarted:
		var msg protocol.SessionStarted
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		// Update DB status.
		h.store.UpdateSessionStatus(context.Background(), msg.SessionID, "running")
		h.broadcastToUserBrowsersByUserID(hc.UserID, data)

	case protocol.TypeSessionStopped:
		var msg protocol.SessionStopped
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		h.store.StopSession(context.Background(), msg.SessionID, msg.ExitCode)
		h.broadcastToUserBrowsersByUserID(hc.UserID, data)
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
		h.handleApprovalResponse(&msg)
	case protocol.TypeStdinData:
		var msg protocol.StdinData
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		sess, err := h.store.GetSession(context.Background(), msg.SessionID)
		if err != nil || sess == nil || sess.UserID != bc.UserID {
			return
		}
		h.SendToHost(sess.HostID, &msg)
	case protocol.TypeSessionResize:
		var msg protocol.SessionResize
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		sess, err := h.store.GetSession(context.Background(), msg.SessionID)
		if err != nil || sess == nil || sess.UserID != bc.UserID {
			return
		}
		h.SendToHost(sess.HostID, &msg)
	}
}

// ── Approval flow ─────────────────────────────────────────────────────────────

func (h *Hub) handleApprovalRequest(hc *HostConn, msg *protocol.ApprovalRequest) {
	// Persist to DB via callback (set by server to avoid import cycle).
	if h.onApproval != nil {
		h.onApproval(context.Background(), msg, hc.UserID)
	}

	// Track token state if the message carries context window data.
	if msg.ContextWindowSize > 0 {
		h.sessionTokensMu.Lock()
		h.sessionTokens[msg.SessionID] = &sessionTokenState{
			InputTokens:       msg.InputTokens,
			ContextWindowSize: msg.ContextWindowSize,
			UserID:            hc.UserID,
		}
		h.sessionTokensMu.Unlock()
		// Broadcast a lightweight token update so browsers update their bars.
		h.broadcastToUserBrowsersByUserID(hc.UserID, mustJSON(map[string]any{
			"type":              "session_tokens",
			"sessionId":         msg.SessionID,
			"inputTokens":       msg.InputTokens,
			"contextWindowSize": msg.ContextWindowSize,
		}))
	}

	// Register pending channel.
	ch := make(chan *protocol.ApprovalResponse, 1)
	h.pendingApprovalsMu.Lock()
	h.pendingApprovals[msg.RequestID] = ch
	h.pendingApprovalsMu.Unlock()

	// Push to all browser clients for this user.
	h.broadcastToUserBrowsersByUserID(hc.UserID, mustJSON(msg))
}

// PushTokenStates sends the current token state for all of a user's sessions
// to a single browser connection (called when a browser first connects).
func (h *Hub) PushTokenStates(bc *BrowserConn) {
	h.sessionTokensMu.RLock()
	defer h.sessionTokensMu.RUnlock()
	for sessionID, st := range h.sessionTokens {
		if st.UserID != bc.UserID {
			continue
		}
		msg := mustJSON(map[string]any{
			"type":              "session_tokens",
			"sessionId":         sessionID,
			"inputTokens":       st.InputTokens,
			"contextWindowSize": st.ContextWindowSize,
		})
		select {
		case bc.send <- msg:
		default:
		}
	}
}

func (h *Hub) handleApprovalResponse(msg *protocol.ApprovalResponse) {
	h.pendingApprovalsMu.Lock()
	ch, ok := h.pendingApprovals[msg.RequestID]
	if ok {
		delete(h.pendingApprovals, msg.RequestID)
	}
	h.pendingApprovalsMu.Unlock()
	if ok {
		select {
		case ch <- msg:
		default:
		}
	}
}

// DeliverApprovalToHost sends an approval decision to the host and unblocks
// any pending hook shim waiting on the in-memory channel.
func (h *Hub) DeliverApprovalToHost(hostID string, resp *protocol.ApprovalResponse) {
	h.SendToHost(hostID, resp)
	h.handleApprovalResponse(resp)
}

// ── Broadcast helpers ─────────────────────────────────────────────────────────

func (h *Hub) broadcastToUserBrowsersByUserID(userID string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, bc := range h.browsers {
		if bc.UserID == userID {
			select {
			case bc.send <- data:
			default:
			}
		}
	}
}

// BroadcastToUser sends a JSON message to all browsers of a user.
func (h *Hub) BroadcastToUser(userID string, msg any) {
	if userID == "" {
		return
	}
	h.broadcastToUserBrowsersByUserID(userID, mustJSON(msg))
}

func (h *Hub) hostUserID(hostID string) string {
	h.mu.RLock()
	hc, ok := h.hosts[hostID]
	h.mu.RUnlock()
	if !ok {
		return ""
	}
	return hc.UserID
}

// ── Graceful shutdown ─────────────────────────────────────────────────────────

// Shutdown notifies all connected clients and closes connections.
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
	}
	for _, bc := range h.browsers {
		select {
		case bc.send <- msg:
		default:
		}
	}
}

// ── Utilities ─────────────────────────────────────────────────────────────────

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
