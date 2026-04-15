package protocol

// Message types for tunnel communication
const (
	TypeHTTPRequest  = "http_request"
	TypeHTTPResponse = "http_response"
	TypeRegister     = "register"
	TypeRegistered   = "registered"
	TypeError        = "error"
	TypePing         = "ping"
	TypePong         = "pong"
)

// TunnelMessage is the envelope for all tunnel communication over WebSocket.
type TunnelMessage struct {
	Type      string            `json:"type"`
	RequestID string            `json:"request_id,omitempty"`
	Subdomain string            `json:"subdomain,omitempty"`
	LocalPort int               `json:"local_port,omitempty"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      []byte            `json:"body,omitempty"`
	Status    int               `json:"status,omitempty"`
	Message   string            `json:"message,omitempty"`
	AuthToken string            `json:"auth_token,omitempty"`
}
