package v1

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AcceptFunc func(conn net.Conn) error

type Server struct {
	*http.Server
	path       string
	acceptFunc AcceptFunc
}

func NewServer(addr, path string, acceptFunc AcceptFunc) *Server {
	s := &Server{
		Server: &http.Server{
			Addr: addr,
		},
		path:       path,
		acceptFunc: acceptFunc,
	}
	
	mux := http.NewServeMux()
	mux.HandleFunc(path, NewHandler(acceptFunc))
	s.Server.Handler = mux
	
	return s
}

func (s *Server) Start() error {
	return s.ListenAndServe()
}

func Connect(ctx context.Context, wsURL string) (net.Conn, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, err
	}

	var port string
	if u.Port() == "" {
		if u.Scheme == "wss" {
			port = "443"
		} else {
			port = "80"
		}
	} else {
		port = u.Port()
	}

	addr := net.JoinHostPort(u.Hostname(), port)
	
	var conn net.Conn
	if u.Scheme == "wss" {
		// TODO: implement TLS support
		return nil, fmt.Errorf("wss not yet supported")
	} else {
		conn, err = net.DialTimeout("tcp", addr, 0)
		if err != nil {
			return nil, err
		}
	}

	// Generate WebSocket key
	key := make([]byte, 16)
	rand.Read(key)
	wsKey := base64.StdEncoding.EncodeToString(key)

	// Send WebSocket handshake
	path := u.Path
	if path == "" {
		path = "/"
	}
	
	handshake := fmt.Sprintf(
		"GET %s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Key: %s\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"\r\n",
		path, u.Host, wsKey)

	if _, err := conn.Write([]byte(handshake)); err != nil {
		conn.Close()
		return nil, err
	}

	// Read and validate response
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}

	if resp.StatusCode != 101 {
		conn.Close()
		return nil, fmt.Errorf("handshake failed: %d", resp.StatusCode)
	}

	// Validate Sec-WebSocket-Accept
	acceptKey := resp.Header.Get("Sec-WebSocket-Accept")
	expectedKey := computeAcceptKey(wsKey)
	if acceptKey != expectedKey {
		conn.Close()
		return nil, fmt.Errorf("invalid accept key")
	}

	return &wsConn{conn: conn, isClient: true}, nil
}

func NewHandler(acceptFunc AcceptFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isWebSocketUpgrade(r) {
			http.Error(w, "Not a WebSocket upgrade", http.StatusBadRequest)
			return
		}

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "WebSocket upgrade not supported", http.StatusInternalServerError)
			return
		}

		conn, _, err := hijacker.Hijack()
		if err != nil {
			http.Error(w, "Failed to hijack connection", http.StatusInternalServerError)
			return
		}

		wsKey := r.Header.Get("Sec-WebSocket-Key")
		if wsKey == "" {
			conn.Close()
			return
		}

		acceptKey := computeAcceptKey(wsKey)
		response := fmt.Sprintf(
			"HTTP/1.1 101 Switching Protocols\r\n"+
				"Upgrade: websocket\r\n"+
				"Connection: Upgrade\r\n"+
				"Sec-WebSocket-Accept: %s\r\n"+
				"\r\n", acceptKey)

		if _, err := conn.Write([]byte(response)); err != nil {
			conn.Close()
			return
		}

		wsConn := &wsConn{conn: conn, isClient: false}
		go acceptFunc(wsConn)
	}
}

const websocketMagicString = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func computeAcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key + websocketMagicString))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.ToLower(r.Header.Get("Connection")) == "upgrade" &&
		strings.ToLower(r.Header.Get("Upgrade")) == "websocket" &&
		r.Header.Get("Sec-WebSocket-Key") != "" &&
		r.Header.Get("Sec-WebSocket-Version") == "13"
}

type wsConn struct {
	conn     net.Conn
	isClient bool
}

func (w *wsConn) Read(b []byte) (int, error) {
	frame, err := w.readFrame()
	if err != nil {
		return 0, err
	}
	
	if len(frame.payload) > len(b) {
		copy(b, frame.payload[:len(b)])
		return len(b), nil
	}
	
	copy(b, frame.payload)
	return len(frame.payload), nil
}

func (w *wsConn) Write(b []byte) (int, error) {
	err := w.writeFrame(1, b) // opcode 1 = text frame
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (w *wsConn) Close() error {
	w.writeFrame(8, []byte{}) // opcode 8 = close frame
	return w.conn.Close()
}

func (w *wsConn) LocalAddr() net.Addr {
	return w.conn.LocalAddr()
}

func (w *wsConn) RemoteAddr() net.Addr {
	return w.conn.RemoteAddr()
}

func (w *wsConn) SetDeadline(t time.Time) error {
	return w.conn.SetDeadline(t)
}

func (w *wsConn) SetReadDeadline(t time.Time) error {
	return w.conn.SetReadDeadline(t)
}

func (w *wsConn) SetWriteDeadline(t time.Time) error {
	return w.conn.SetWriteDeadline(t)
}

type wsFrame struct {
	fin     bool
	opcode  byte
	masked  bool
	payload []byte
}

func (w *wsConn) readFrame() (*wsFrame, error) {
	var header [2]byte
	if _, err := io.ReadFull(w.conn, header[:]); err != nil {
		return nil, err
	}

	frame := &wsFrame{
		fin:    (header[0] & 0x80) != 0,
		opcode: header[0] & 0x0F,
		masked: (header[1] & 0x80) != 0,
	}

	payloadLen := uint64(header[1] & 0x7F)
	
	if payloadLen == 126 {
		var extended [2]byte
		if _, err := io.ReadFull(w.conn, extended[:]); err != nil {
			return nil, err
		}
		payloadLen = uint64(binary.BigEndian.Uint16(extended[:]))
	} else if payloadLen == 127 {
		var extended [8]byte
		if _, err := io.ReadFull(w.conn, extended[:]); err != nil {
			return nil, err
		}
		payloadLen = binary.BigEndian.Uint64(extended[:])
	}

	var maskKey [4]byte
	if frame.masked {
		if _, err := io.ReadFull(w.conn, maskKey[:]); err != nil {
			return nil, err
		}
	}

	frame.payload = make([]byte, payloadLen)
	if _, err := io.ReadFull(w.conn, frame.payload); err != nil {
		return nil, err
	}

	if frame.masked {
		for i := range frame.payload {
			frame.payload[i] ^= maskKey[i%4]
		}
	}

	return frame, nil
}

func (w *wsConn) writeFrame(opcode byte, payload []byte) error {
	payloadLen := len(payload)
	
	var header []byte
	header = append(header, 0x80|opcode) // FIN=1, opcode
	
	var maskBit byte
	if w.isClient {
		maskBit = 0x80 // Client frames must be masked
	}
	
	if payloadLen < 126 {
		header = append(header, byte(payloadLen)|maskBit)
	} else if payloadLen < 65536 {
		header = append(header, 126|maskBit)
		var extended [2]byte
		binary.BigEndian.PutUint16(extended[:], uint16(payloadLen))
		header = append(header, extended[:]...)
	} else {
		header = append(header, 127|maskBit)
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(payloadLen))
		header = append(header, extended[:]...)
	}

	var maskKey [4]byte
	if w.isClient {
		// Generate masking key for client frames
		rand.Read(maskKey[:])
		header = append(header, maskKey[:]...)
	}

	if _, err := w.conn.Write(header); err != nil {
		return err
	}
	
	if payloadLen > 0 {
		if w.isClient {
			// Mask the payload for client frames
			maskedPayload := make([]byte, payloadLen)
			for i := 0; i < payloadLen; i++ {
				maskedPayload[i] = payload[i] ^ maskKey[i%4]
			}
			
			if _, err := w.conn.Write(maskedPayload); err != nil {
				return err
			}
		} else {
			// Server frames are not masked
			if _, err := w.conn.Write(payload); err != nil {
				return err
			}
		}
	}
	
	return nil
}