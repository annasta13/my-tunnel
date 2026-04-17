package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"mytunnel/pkg/protocol"

	"github.com/gorilla/websocket"
)

var (
	serverURL string
	authToken string
)

// TunnelEntry represents a managed tunnel visible in the UI.
type TunnelEntry struct {
	Subdomain string `json:"subdomain"`
	LocalPort int    `json:"local_port"`
	Status    string `json:"status"` // "active", "paused", "disconnected"
	PublicURL string `json:"public_url,omitempty"`
	conn      *websocket.Conn
	stopCh    chan struct{}
	mu        sync.Mutex
}

var (
	entries   = make(map[string]*TunnelEntry) // subdomain -> entry
	entriesMu sync.RWMutex
)

func main() {
	flag.StringVar(&serverURL, "server", "ws://tunnel.example.com:7891", "Tunnel server WebSocket URL")
	flag.StringVar(&authToken, "token", "changeme", "Auth token")
	uiAddr := flag.String("ui", ":9000", "UI dashboard listen address")
	flag.Parse()

	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/tunnels", handleTunnels)
	mux.HandleFunc("/api/tunnels/create", handleCreate)
	mux.HandleFunc("/api/tunnels/pause", handlePause)
	mux.HandleFunc("/api/tunnels/resume", handleResume)
	mux.HandleFunc("/api/tunnels/delete", handleDelete)

	// Serve UI
	mux.HandleFunc("/", serveUI)

	log.Printf("Dashboard running at http://localhost%s", *uiAddr)
	log.Fatal(http.ListenAndServe(*uiAddr, mux))
}

// --- API Handlers ---

func handleTunnels(w http.ResponseWriter, r *http.Request) {
	entriesMu.RLock()
	list := make([]*TunnelEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, e)
	}
	entriesMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

type createReq struct {
	Subdomain string `json:"subdomain"`
	LocalPort int    `json:"local_port"`
}

func handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	entriesMu.Lock()
	if _, exists := entries[req.Subdomain]; exists {
		entriesMu.Unlock()
		http.Error(w, "Subdomain already exists", http.StatusConflict)
		return
	}

	entry := &TunnelEntry{
		Subdomain: req.Subdomain,
		LocalPort: req.LocalPort,
		Status:    "connecting",
		stopCh:    make(chan struct{}),
	}
	entries[req.Subdomain] = entry
	entriesMu.Unlock()

	go connectTunnel(entry)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry)
}

func handlePause(w http.ResponseWriter, r *http.Request) {
	sub := r.URL.Query().Get("subdomain")
	entriesMu.RLock()
	entry, ok := entries[sub]
	entriesMu.RUnlock()
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	entry.mu.Lock()
	if entry.conn != nil {
		entry.conn.Close()
		entry.conn = nil
	}
	if entry.stopCh != nil {
		close(entry.stopCh)
		entry.stopCh = nil
	}
	entry.Status = "paused"
	entry.mu.Unlock()
	json.NewEncoder(w).Encode(entry)
}

func handleResume(w http.ResponseWriter, r *http.Request) {
	sub := r.URL.Query().Get("subdomain")
	entriesMu.RLock()
	entry, ok := entries[sub]
	entriesMu.RUnlock()
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	entry.mu.Lock()
	entry.Status = "connecting"
	entry.stopCh = make(chan struct{})
	entry.mu.Unlock()
	go connectTunnel(entry)
	json.NewEncoder(w).Encode(entry)
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	sub := r.URL.Query().Get("subdomain")
	entriesMu.Lock()
	entry, ok := entries[sub]
	if ok {
		entry.mu.Lock()
		if entry.conn != nil {
			entry.conn.Close()
		}
		if entry.stopCh != nil {
			close(entry.stopCh)
		}
		entry.mu.Unlock()
		delete(entries, sub)
	}
	entriesMu.Unlock()
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"ok":true}`)
}

// --- Tunnel Connection ---

func connectTunnel(entry *TunnelEntry) {
	wsURL := serverURL + "/_tunnel/register"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		log.Printf("[%s] Connection failed: %v", entry.Subdomain, err)
		entry.mu.Lock()
		entry.Status = "disconnected"
		entry.mu.Unlock()
		go retryConnect(entry)
		return
	}

	// Send registration
	regMsg := protocol.TunnelMessage{
		Type:      protocol.TypeRegister,
		Subdomain: entry.Subdomain,
		LocalPort: entry.LocalPort,
		AuthToken: authToken,
	}
	if err := conn.WriteJSON(regMsg); err != nil {
		conn.Close()
		entry.mu.Lock()
		entry.Status = "disconnected"
		entry.mu.Unlock()
		return
	}

	// Read registration response
	var resp protocol.TunnelMessage
	if err := conn.ReadJSON(&resp); err != nil || resp.Type == protocol.TypeError {
		log.Printf("[%s] Registration failed: %s", entry.Subdomain, resp.Message)
		conn.Close()
		entry.mu.Lock()
		entry.Status = "disconnected"
		entry.mu.Unlock()
		return
	}

	entry.mu.Lock()
	entry.conn = conn
	entry.Status = "active"
	entry.PublicURL = resp.Message
	entry.mu.Unlock()

	log.Printf("[%s] Connected → %s", entry.Subdomain, resp.Message)

	// Start keepalive ping (prevents Cloudflare 100s idle timeout)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				entry.mu.Lock()
				c := entry.conn
				entry.mu.Unlock()
				if c == nil {
					return
				}
				entry.mu.Lock()
				c.WriteJSON(protocol.TunnelMessage{Type: protocol.TypePing})
				entry.mu.Unlock()
			case <-entry.stopCh:
				return
			}
		}
	}()

	// Handle incoming requests from server
	for {
		var msg protocol.TunnelMessage
		if err := conn.ReadJSON(&msg); err != nil {
			log.Printf("[%s] Read error: %v", entry.Subdomain, err)
			break
		}

		if msg.Type == protocol.TypeHTTPRequest {
			go handleProxiedRequest(entry, conn, msg)
		}
	}

	entry.mu.Lock()
	entry.conn = nil
	wasPaused := entry.Status == "paused"
	if !wasPaused {
		entry.Status = "disconnected"
	}
	entry.mu.Unlock()

	if !wasPaused {
		go retryConnect(entry)
	}
}

func retryConnect(entry *TunnelEntry) {
	entry.mu.Lock()
	stopCh := entry.stopCh
	entry.mu.Unlock()
	if stopCh == nil {
		return
	}

	select {
	case <-time.After(3 * time.Second):
		entry.mu.Lock()
		status := entry.Status
		entry.mu.Unlock()
		if status == "disconnected" {
			connectTunnel(entry)
		}
	case <-stopCh:
		return
	}
}

func handleProxiedRequest(entry *TunnelEntry, conn *websocket.Conn, msg protocol.TunnelMessage) {
	url := fmt.Sprintf("http://localhost:%d%s", entry.LocalPort, msg.Path)
	req, err := http.NewRequest(msg.Method, url, bytes.NewReader(msg.Body))
	if err != nil {
		sendErrorResponse(conn, msg.RequestID, 502, "Bad request construction")
		return
	}
	for k, v := range msg.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		sendErrorResponse(conn, msg.RequestID, 502, fmt.Sprintf("Local service error: %v", err))
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	headers := make(map[string]string)
	for k, v := range resp.Header {
		headers[k] = v[0]
	}

	entry.mu.Lock()
	conn.WriteJSON(protocol.TunnelMessage{
		Type:      protocol.TypeHTTPResponse,
		RequestID: msg.RequestID,
		Status:    resp.StatusCode,
		Headers:   headers,
		Body:      body,
	})
	entry.mu.Unlock()
}

func sendErrorResponse(conn *websocket.Conn, reqID string, status int, message string) {
	conn.WriteJSON(protocol.TunnelMessage{
		Type:      protocol.TypeHTTPResponse,
		RequestID: reqID,
		Status:    status,
		Body:      []byte(message),
		Headers:   map[string]string{"Content-Type": "text/plain"},
	})
}

// --- Embedded UI ---

func serveUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, uiHTML)
}

const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>MyTunnel Dashboard</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; }
  .container { max-width: 900px; margin: 0 auto; padding: 2rem; }
  h1 { font-size: 1.8rem; margin-bottom: 0.5rem; color: #38bdf8; }
  .subtitle { color: #64748b; margin-bottom: 2rem; }
  .card { background: #1e293b; border-radius: 12px; padding: 1.5rem; margin-bottom: 1rem; border: 1px solid #334155; }
  .form-row { display: flex; gap: 0.75rem; margin-bottom: 1.5rem; }
  input { background: #0f172a; border: 1px solid #334155; color: #e2e8f0; padding: 0.6rem 1rem; border-radius: 8px; font-size: 0.9rem; flex: 1; }
  input:focus { outline: none; border-color: #38bdf8; }
  input::placeholder { color: #475569; }
  button { padding: 0.6rem 1.2rem; border-radius: 8px; border: none; cursor: pointer; font-size: 0.85rem; font-weight: 600; transition: all 0.15s; }
  .btn-primary { background: #38bdf8; color: #0f172a; }
  .btn-primary:hover { background: #7dd3fc; }
  .btn-warn { background: #f59e0b; color: #0f172a; }
  .btn-warn:hover { background: #fbbf24; }
  .btn-success { background: #22c55e; color: #0f172a; }
  .btn-success:hover { background: #4ade80; }
  .btn-danger { background: #ef4444; color: white; }
  .btn-danger:hover { background: #f87171; }
  .btn-sm { padding: 0.35rem 0.75rem; font-size: 0.8rem; }
  .tunnel-item { display: flex; align-items: center; justify-content: space-between; padding: 1rem; background: #1e293b; border-radius: 10px; margin-bottom: 0.75rem; border: 1px solid #334155; }
  .tunnel-info { flex: 1; }
  .tunnel-name { font-weight: 600; font-size: 1rem; color: #f1f5f9; }
  .tunnel-url { font-size: 0.85rem; color: #38bdf8; margin-top: 0.2rem; }
  .tunnel-port { font-size: 0.85rem; color: #94a3b8; }
  .tunnel-actions { display: flex; gap: 0.5rem; }
  .status { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 0.5rem; }
  .status-active { background: #22c55e; box-shadow: 0 0 6px #22c55e; }
  .status-paused { background: #f59e0b; }
  .status-disconnected { background: #ef4444; }
  .status-connecting { background: #64748b; animation: pulse 1s infinite; }
  @keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.3; } }
  .empty { text-align: center; color: #475569; padding: 3rem; }
</style>
</head>
<body>
<div class="container">
  <h1>🚇 MyTunnel</h1>
  <p class="subtitle">Self-hosted tunnel manager</p>

  <div class="card">
    <div class="form-row">
      <input type="text" id="subdomain" placeholder="Subdomain (e.g. odoo)">
      <input type="number" id="port" placeholder="Local port (e.g. 8069)">
      <button class="btn-primary" onclick="createTunnel()">+ Add Tunnel</button>
    </div>
  </div>

  <div id="tunnels"><div class="empty">No tunnels yet. Add one above.</div></div>
</div>

<script>
async function api(path, opts) {
  const res = await fetch('/api/tunnels' + path, opts);
  return res.json();
}

async function loadTunnels() {
  const list = await api('');
  const el = document.getElementById('tunnels');
  if (!list || list.length === 0) {
    el.innerHTML = '<div class="empty">No tunnels yet. Add one above.</div>';
    return;
  }
  el.innerHTML = list.map(t => {
    const statusClass = 'status-' + t.status;
    const isPaused = t.status === 'paused';
    return '<div class="tunnel-item">' +
      '<div class="tunnel-info">' +
        '<div class="tunnel-name"><span class="status ' + statusClass + '"></span>' + t.subdomain + '</div>' +
        (t.public_url ? '<div class="tunnel-url">' + t.public_url + '</div>' : '') +
        '<div class="tunnel-port">→ localhost:' + t.local_port + ' &middot; ' + t.status + '</div>' +
      '</div>' +
      '<div class="tunnel-actions">' +
        (isPaused
          ? '<button class="btn-success btn-sm" onclick="resumeTunnel(\'' + t.subdomain + '\')">▶ Resume</button>'
          : '<button class="btn-warn btn-sm" onclick="pauseTunnel(\'' + t.subdomain + '\')">⏸ Pause</button>') +
        '<button class="btn-danger btn-sm" onclick="deleteTunnel(\'' + t.subdomain + '\')">✕</button>' +
      '</div>' +
    '</div>';
  }).join('');
}

async function createTunnel() {
  const subdomain = document.getElementById('subdomain').value.trim();
  const port = parseInt(document.getElementById('port').value);
  if (!subdomain || !port) return alert('Fill in both fields');
  await api('/create', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({subdomain, local_port: port})
  });
  document.getElementById('subdomain').value = '';
  document.getElementById('port').value = '';
  loadTunnels();
}

async function pauseTunnel(sub) { await api('/pause?subdomain=' + sub); loadTunnels(); }
async function resumeTunnel(sub) { await api('/resume?subdomain=' + sub); loadTunnels(); }
async function deleteTunnel(sub) { await api('/delete?subdomain=' + sub); loadTunnels(); }

loadTunnels();
setInterval(loadTunnels, 3000);
</script>
</body>
</html>`
