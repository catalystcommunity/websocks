package v1

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestComputeAcceptKey(t *testing.T) {
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	expected := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	
	result := computeAcceptKey(key)
	if result != expected {
		t.Errorf("computeAcceptKey() = %v, want %v", result, expected)
	}
}

func TestIsWebSocketUpgrade(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		expected bool
	}{
		{
			name: "valid websocket upgrade",
			headers: map[string]string{
				"Connection":             "upgrade",
				"Upgrade":               "websocket",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version": "13",
			},
			expected: true,
		},
		{
			name: "missing connection header",
			headers: map[string]string{
				"Upgrade":               "websocket",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version": "13",
			},
			expected: false,
		},
		{
			name: "wrong websocket version",
			headers: map[string]string{
				"Connection":             "upgrade",
				"Upgrade":               "websocket",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version": "12",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			
			result := isWebSocketUpgrade(req)
			if result != tt.expected {
				t.Errorf("isWebSocketUpgrade() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestNewServer(t *testing.T) {
	acceptFunc := func(conn net.Conn) error {
		return nil
	}
	
	server := NewServer(":8080", "/ws", acceptFunc)
	
	if server.Addr != ":8080" {
		t.Errorf("Server.Addr = %v, want :8080", server.Addr)
	}
	
	if server.path != "/ws" {
		t.Errorf("Server.path = %v, want /ws", server.path)
	}
}

func TestNewHandler(t *testing.T) {
	acceptFunc := func(conn net.Conn) error {
		return nil
	}
	
	handler := NewHandler(acceptFunc)
	
	// Test non-websocket request
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	
	handler(w, req)
	
	if w.Code != http.StatusBadRequest {
		t.Errorf("Handler returned %v, want %v", w.Code, http.StatusBadRequest)
	}
	
	if !strings.Contains(w.Body.String(), "Not a WebSocket upgrade") {
		t.Errorf("Handler returned unexpected body: %v", w.Body.String())
	}
}