package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"mytunnel/pkg/protocol"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var (
	baseDomain string
	authToken  string
	upgrader   = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
)

// tunnel represents a single registered tunnel from a client.
type tunnel struct {
	subdomain string
	localPort int
	conn      *websocket.Conn
	mu        sync.Mutex
	pending   map[string]chan *protocol.TunnelMessage
	pendingMu sync.Mutex
}

var (
	tunnels   = make(map[string]*tunnel) // subdomain -> tunnel
	tunnelsMu sync.RWMutex
)

func main() {
	flag.StringVar(&baseDomain, "domain", "tunnel.example.com", "Base domain for tunnels")
	flag.StringVar(&authToken, "token", "changeme", "Auth token for client connections")
	addr := flag.String("addr", ":7891", "Listen address")
	flag.Parse()

	http.HandleFunc("/", handleRequest)
	log.Printf("Tunnel server starting on %s (domain: *.%s)", *addr, baseDomain)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// handleRequest routes incoming requests: WebSocket registration or HTTP proxy.
func handleRequest(w http.ResponseWriter, r *http.Request) {
	host := strings.Split(r.Host, ":")[0]

	// Client registration endpoint
	if r.URL.Path == "/_tunnel/register" && websocket.IsWebSocketUpgrade(r) {
		handleRegister(w, r)
		return
	}

	// Extract subdomain
	if !strings.HasSuffix(host, "."+baseDomain) {
		http.Error(w, "Unknown host", http.StatusNotFound)
		return
	}
	subdomain := strings.TrimSuffix(host, "."+baseDomain)

	tunnelsMu.RLock()
	t, ok := tunnels[subdomain]
	tunnelsMu.RUnlock()
	if !ok {
		http.Error(w, fmt.Sprintf("Tunnel '%s' not found or paused", subdomain), http.StatusBadGateway)
		return
	}

	proxyToTunnel(w, r, t)
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	// Read registration message
	var msg protocol.TunnelMessage
	if err := conn.ReadJSON(&msg); err != nil {
		conn.Close()
		return
	}
	if msg.Type != protocol.TypeRegister {
		conn.WriteJSON(protocol.TunnelMessage{Type: protocol.TypeError, Message: "expected register"})
		conn.Close()
		return
	}
	if msg.AuthToken != authToken {
		conn.WriteJSON(protocol.TunnelMessage{Type: protocol.TypeError, Message: "invalid token"})
		conn.Close()
		return
	}

	t := &tunnel{
		subdomain: msg.Subdomain,
		localPort: msg.LocalPort,
		conn:      conn,
		pending:   make(map[string]chan *protocol.TunnelMessage),
	}

	tunnelsMu.Lock()
	tunnels[msg.Subdomain] = t
	tunnelsMu.Unlock()

	conn.WriteJSON(protocol.TunnelMessage{
		Type:      protocol.TypeRegistered,
		Subdomain: msg.Subdomain,
		Message:   fmt.Sprintf("https://%s.%s", msg.Subdomain, baseDomain),
	})

	log.Printf("Tunnel registered: %s → localhost:%d", msg.Subdomain, msg.LocalPort)

	// Read responses from client
	go t.readLoop()
}

func (t *tunnel) readLoop() {
	defer func() {
		tunnelsMu.Lock()
		delete(tunnels, t.subdomain)
		tunnelsMu.Unlock()
		t.conn.Close()
		log.Printf("Tunnel disconnected: %s", t.subdomain)
	}()

	for {
		var msg protocol.TunnelMessage
		if err := t.conn.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypeHTTPResponse:
			t.pendingMu.Lock()
			ch, ok := t.pending[msg.RequestID]
			if ok {
				ch <- &msg
				delete(t.pending, msg.RequestID)
			}
			t.pendingMu.Unlock()
		case protocol.TypePong:
			// keepalive response, ignore
		}
	}
}

func proxyToTunnel(w http.ResponseWriter, r *http.Request, t *tunnel) {
	reqID := uuid.New().String()

	body, _ := io.ReadAll(r.Body)
	headers := make(map[string]string)
	for k, v := range r.Header {
		headers[k] = strings.Join(v, ", ")
	}

	msg := protocol.TunnelMessage{
		Type:      protocol.TypeHTTPRequest,
		RequestID: reqID,
		Method:    r.Method,
		Path:      r.URL.RequestURI(),
		Headers:   headers,
		Body:      body,
	}

	respCh := make(chan *protocol.TunnelMessage, 1)
	t.pendingMu.Lock()
	t.pending[reqID] = respCh
	t.pendingMu.Unlock()

	t.mu.Lock()
	err := t.conn.WriteJSON(msg)
	t.mu.Unlock()
	if err != nil {
		http.Error(w, "Tunnel write error", http.StatusBadGateway)
		return
	}

	select {
	case resp := <-respCh:
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.Status)
		w.Write(resp.Body)
	case <-time.After(30 * time.Second):
		t.pendingMu.Lock()
		delete(t.pending, reqID)
		t.pendingMu.Unlock()
		http.Error(w, "Tunnel timeout", http.StatusGatewayTimeout)
	}
}
