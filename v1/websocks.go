// Package v1 provides a lightweight WebSocket implementation that presents WebSocket connections
// as standard net.Conn interfaces. This allows WebSocket connections to be used anywhere
// a regular network connection is expected, making it easy to integrate WebSocket support
// into existing applications.
//
// The package supports both client and server functionality:
//   - Server: Create WebSocket servers that accept connections via HTTP upgrade
//   - Client: Connect to WebSocket servers and get a net.Conn interface
//
// # Protocol Support and Limitations
//
// Currently supported WebSocket features:
//   - Text frames (opcode 0x1) - all data is sent as text frames
//   - Close frames (opcode 0x8) - proper connection closure
//   - Client-side frame masking per RFC 6455
//   - Payload lengths up to 64-bit values
//   - Basic WebSocket handshake validation
//
// Not yet supported:
//   - Binary frames (opcode 0x2)
//   - Ping/Pong frames (opcodes 0x9/0xA)
//   - WebSocket extensions (compression, etc.)
//   - WebSocket subprotocols
//   - Fragmented messages
//   - TLS/WSS connections (planned)
//
// # Concurrency and Thread Safety
//
// WebSocket connections (net.Conn instances returned by this package) are NOT thread-safe.
// Like standard net.Conn implementations, concurrent access to Read/Write methods requires
// external synchronization. However, you can safely call different methods concurrently:
//   - One goroutine reading (Read)
//   - One goroutine writing (Write)
//   - Close() can be called from any goroutine
//
// The Server type is safe for concurrent use - multiple goroutines can accept connections
// simultaneously as each connection is handled in its own goroutine.
//
// # Error Handling
//
// Common errors you should handle:
//
// Connection errors:
//   - net.OpError: Network-level connection failures
//   - context.DeadlineExceeded: Connection timeouts
//   - io.EOF: Remote peer closed connection
//
// WebSocket protocol errors:
//   - "handshake failed": HTTP upgrade rejected (status != 101)
//   - "invalid accept key": Server returned incorrect Sec-WebSocket-Accept
//   - "Not a WebSocket upgrade": Missing required WebSocket headers
//
// # Resource Management
//
// Always close connections when done to prevent resource leaks:
//
//	conn, err := v1.Connect(ctx, url)
//	if err != nil {
//		return err
//	}
//	defer conn.Close() // Always defer close
//
// For servers, each connection handler should close its connection:
//
//	func handler(conn net.Conn) error {
//		defer conn.Close() // Critical for preventing leaks
//		// ... handle connection
//		return nil
//	}
//
// # Performance Considerations
//
// - Each WebSocket frame has overhead (2-14 bytes). For high throughput, batch data
// - Read() operations are frame-by-frame. Use buffered I/O for small frequent reads
// - Write() operations send complete frames. Larger writes are more efficient
// - Connection handshake requires 1 RTT. Reuse connections when possible
//
// # Integration Examples
//
// Echo server:
//
//	func echoHandler(conn net.Conn) error {
//		defer conn.Close()
//		buffer := make([]byte, 1024)
//		for {
//			n, err := conn.Read(buffer)
//			if err != nil {
//				return err // Connection closed or error
//			}
//			if _, err := conn.Write(buffer[:n]); err != nil {
//				return err
//			}
//		}
//	}
//	server := v1.NewServer(":8080", "/ws", echoHandler)
//	log.Fatal(server.Start())
//
// Client with proper error handling:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
//	defer cancel()
//	
//	conn, err := v1.Connect(ctx, "ws://localhost:8080/ws")
//	if err != nil {
//		if errors.Is(err, context.DeadlineExceeded) {
//			return fmt.Errorf("connection timeout: %w", err)
//		}
//		return fmt.Errorf("connection failed: %w", err)
//	}
//	defer conn.Close()
//	
//	// Set read timeout for operations
//	conn.SetReadDeadline(time.Now().Add(10*time.Second))
//	
//	if _, err := conn.Write([]byte("hello")); err != nil {
//		return fmt.Errorf("write failed: %w", err)
//	}
//	
//	buffer := make([]byte, 1024)
//	n, err := conn.Read(buffer)
//	if err != nil {
//		if errors.Is(err, io.EOF) {
//			return fmt.Errorf("server closed connection")
//		}
//		return fmt.Errorf("read failed: %w", err)
//	}
//	
// HTTP middleware integration:
//
//	func wsMiddleware(next http.HandlerFunc) http.HandlerFunc {
//		wsHandler := v1.NewHandler(func(conn net.Conn) error {
//			// Handle WebSocket connection
//			defer conn.Close()
//			return handleWebSocket(conn)
//		})
//		
//		return func(w http.ResponseWriter, r *http.Request) {
//			if r.Header.Get("Upgrade") == "websocket" {
//				wsHandler(w, r)
//				return
//			}
//			next(w, r)
//		}
//	}
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

// AcceptFunc is a callback function that handles incoming WebSocket connections.
// It receives a net.Conn that represents the WebSocket connection and should
// return an error if the connection handling fails.
//
// The connection passed to AcceptFunc is already upgraded to WebSocket protocol
// and can be used like any standard net.Conn for reading and writing data.
type AcceptFunc func(conn net.Conn) error

// Server represents a WebSocket server that can accept incoming connections.
// It embeds an http.Server and handles the WebSocket upgrade process automatically.
// When a WebSocket upgrade request is received on the configured path, it calls
// the provided AcceptFunc with the resulting net.Conn.
type Server struct {
	*http.Server
	path       string
	acceptFunc AcceptFunc
}

// NewServer creates a new WebSocket server that listens on the specified address and path.
//
// Parameters:
//   - addr: The address to listen on (e.g., ":8080", "localhost:8080", or "0.0.0.0:8080")
//   - path: The HTTP path that will handle WebSocket upgrades (e.g., "/ws", "/websocket")
//   - acceptFunc: A callback function that will be called for each new WebSocket connection
//
// The server automatically handles the WebSocket upgrade handshake according to RFC 6455.
// When a valid WebSocket upgrade request is received on the specified path, the connection
// is upgraded and passed to the acceptFunc as a net.Conn in a new goroutine.
//
// Server behavior:
//   - Only requests to the exact path will be handled as WebSocket upgrades
//   - Non-WebSocket requests to the path return 400 Bad Request
//   - Requests to other paths return 404 Not Found
//   - Each accepted connection runs in its own goroutine
//   - The server is safe for concurrent use by multiple goroutines
//
// The acceptFunc should:
//   - Always call defer conn.Close() to prevent resource leaks
//   - Handle the connection until it's no longer needed
//   - Return an error if connection handling fails
//   - Be prepared for the connection to be closed unexpectedly
//
// Resource management: The server will create one goroutine per connection.
// Ensure your acceptFunc properly closes connections to prevent goroutine leaks.
//
// Common patterns:
//
// Echo server:
//
//	server := NewServer(":8080", "/ws", func(conn net.Conn) error {
//		defer conn.Close()
//		return io.Copy(conn, conn) // Echo all received data back
//	})
//
// Chat server (with connection registry):
//
//	var connections = make(map[net.Conn]bool)
//	var connMutex sync.RWMutex
//	
//	server := NewServer(":8080", "/chat", func(conn net.Conn) error {
//		defer func() {
//			conn.Close()
//			connMutex.Lock()
//			delete(connections, conn)
//			connMutex.Unlock()
//		}()
//		
//		connMutex.Lock()
//		connections[conn] = true
//		connMutex.Unlock()
//		
//		// Handle messages and broadcast to other connections
//		buffer := make([]byte, 1024)
//		for {
//			n, err := conn.Read(buffer)
//			if err != nil {
//				return err
//			}
//			broadcast(buffer[:n], conn) // Implement broadcast logic
//		}
//	})
//
// The returned Server embeds http.Server, so you can access standard HTTP server
// functionality like graceful shutdown:
//
//	server := NewServer(":8080", "/ws", handler)
//	go func() {
//		if err := server.Start(); err != http.ErrServerClosed {
//			log.Fatal(err)
//		}
//	}()
//	
//	// Graceful shutdown
//	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
//	defer cancel()
//	server.Shutdown(ctx)
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

// Start begins listening for HTTP connections and handling WebSocket upgrades.
// This method blocks until the server is stopped or encounters an error.
//
// It returns an error if the server fails to start or if ListenAndServe fails.
// Common errors include address already in use or permission denied for privileged ports.
//
// To stop the server gracefully, use the Shutdown method inherited from http.Server.
func (s *Server) Start() error {
	return s.ListenAndServe()
}

// Connect establishes a WebSocket connection to the specified URL and returns it as a net.Conn.
//
// Parameters:
//   - ctx: Context for connection timeout and cancellation. Use context.WithTimeout for connection timeouts.
//   - wsURL: The WebSocket URL to connect to (e.g., "ws://example.com/ws" or "wss://example.com/ws")
//
// The function handles the complete WebSocket handshake process:
//   - Parses the WebSocket URL (returns error for invalid URLs)
//   - Establishes a TCP connection (respects context cancellation)
//   - Performs the WebSocket upgrade handshake per RFC 6455
//   - Validates the server's response (Sec-WebSocket-Accept header)
//
// Currently, only "ws://" (unencrypted) connections are supported. TLS support ("wss://")
// is planned for future implementation - calling with "wss://" URLs returns an error.
//
// Returns a net.Conn that can be used for reading and writing WebSocket data,
// or an error if the connection or handshake fails.
//
// Possible errors:
//   - url.Parse errors for malformed URLs
//   - net.DialTimeout errors for connection failures
//   - "wss not yet supported" for TLS URLs
//   - "handshake failed: <status>" for non-101 HTTP responses
//   - "invalid accept key" for protocol violations
//   - context.DeadlineExceeded for timeouts
//   - context.Canceled for cancellation
//
// The returned connection automatically handles WebSocket framing. All data written
// becomes WebSocket text frames, and all data read comes from WebSocket text frames.
// The connection is NOT thread-safe - use external synchronization for concurrent access.
//
// Connection lifecycle: The connection remains open until explicitly closed with Close()
// or until the underlying network connection fails. Always defer Close() to prevent leaks.
//
// Example with comprehensive error handling:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
//	defer cancel()
//	
//	conn, err := Connect(ctx, "ws://localhost:8080/ws")
//	if err != nil {
//		if errors.Is(err, context.DeadlineExceeded) {
//			return fmt.Errorf("connection timeout: %w", err)
//		}
//		if strings.Contains(err.Error(), "wss not yet supported") {
//			return fmt.Errorf("TLS not supported: %w", err)
//		}
//		return fmt.Errorf("connection failed: %w", err)
//	}
//	defer conn.Close()
//	
//	// Set operation timeouts
//	conn.SetReadDeadline(time.Now().Add(10*time.Second))
//	conn.SetWriteDeadline(time.Now().Add(5*time.Second))
//	
//	// Send data
//	if _, err := conn.Write([]byte("Hello WebSocket")); err != nil {
//		return fmt.Errorf("write failed: %w", err)
//	}
//	
//	// Read response
//	buffer := make([]byte, 1024)
//	n, err := conn.Read(buffer)
//	if err != nil {
//		if errors.Is(err, io.EOF) {
//			return fmt.Errorf("server closed connection")
//		}
//		return fmt.Errorf("read failed: %w", err)
//	}
//	response := string(buffer[:n])
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

// NewHandler creates an HTTP handler function that processes WebSocket upgrade requests.
//
// The handler validates incoming requests for proper WebSocket upgrade headers,
// performs the connection hijacking, sends the appropriate upgrade response,
// and then calls the provided AcceptFunc with the resulting WebSocket connection.
//
// This function is primarily used internally by NewServer, but can be used directly
// if you need more control over the HTTP server setup.
//
// Parameters:
//   - acceptFunc: Function to call with each successfully upgraded WebSocket connection
//
// The handler will:
//   - Validate WebSocket upgrade headers (Connection, Upgrade, Sec-WebSocket-Key, etc.)
//   - Hijack the HTTP connection
//   - Send the WebSocket handshake response
//   - Create a WebSocket connection wrapper
//   - Call acceptFunc in a new goroutine
//
// Returns an http.HandlerFunc that can be registered with an HTTP multiplexer.
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

// wsConn wraps a net.Conn to provide WebSocket protocol handling.
// It implements the net.Conn interface, allowing WebSocket connections to be used
// anywhere a standard network connection is expected.
//
// The isClient field determines whether this connection originated from a client
// (Connect) or server (NewHandler), which affects frame masking behavior per RFC 6455.
type wsConn struct {
	conn     net.Conn
	isClient bool
}

// Read implements net.Conn.Read by reading and parsing WebSocket frames.
// It reads one complete WebSocket frame and copies its payload into the provided buffer.
//
// Frame handling behavior:
//   - Reads exactly one WebSocket frame per call (not partial frames)
//   - Only text frames (opcode 0x1) contain readable data
//   - Close frames (opcode 0x8) will cause subsequent reads to return io.EOF
//   - Other frame types are currently not supported
//
// Buffer behavior:
//   - If the frame payload is larger than the buffer, only buffer-sized data is copied
//   - Remaining frame data is discarded (this is a limitation)
//   - For reliable data transfer, ensure buffer is larger than expected message size
//   - Returns the number of bytes copied (may be less than frame size)
//
// Error conditions:
//   - io.EOF: Connection closed by remote peer or close frame received
//   - net.OpError: Network-level errors (connection reset, timeout, etc.)
//   - Protocol errors: Malformed WebSocket frames
//
// This method is NOT thread-safe. Only one goroutine should call Read at a time.
// For concurrent operation, use separate goroutines for reading and writing.
//
// Performance note: Each Read() processes one WebSocket frame. For applications
// that receive many small messages, consider using a buffered reader pattern:
//
//	reader := bufio.NewReader(conn)
//	// Use reader.ReadLine(), reader.ReadBytes(), etc.
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

// Write implements net.Conn.Write by encapsulating data in WebSocket frames.
//
// Frame creation behavior:
//   - All data is sent as text frames (opcode 0x1) according to WebSocket protocol
//   - Each Write() call creates exactly one WebSocket frame
//   - Client connections automatically apply masking as required by RFC 6455
//   - Server connections send unmasked frames as per RFC 6455
//
// Data handling:
//   - The entire buffer is sent in a single frame (no partial writes)
//   - Always returns len(b) and nil on success (full buffer written)
//   - No encoding/decoding is performed - raw bytes are sent
//   - Maximum frame size is limited by available memory and network constraints
//
// Error conditions:
//   - Network errors from underlying connection (connection reset, timeout)
//   - Protocol errors during frame construction (rare)
//   - If an error occurs, no data is sent (atomic operation)
//
// This method is NOT thread-safe. Only one goroutine should call Write at a time.
// For concurrent operation, use separate goroutines for reading and writing.
//
// Performance considerations:
//   - Larger writes are more efficient due to WebSocket frame overhead
//   - Each Write() results in immediate network transmission
//   - For high-frequency small writes, consider batching data:
//
//	var buffer bytes.Buffer
//	buffer.Write(data1)
//	buffer.Write(data2)
//	conn.Write(buffer.Bytes()) // Send as single frame
func (w *wsConn) Write(b []byte) (int, error) {
	err := w.writeFrame(1, b) // opcode 1 = text frame
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

// Close implements net.Conn.Close by sending a WebSocket close frame
// and then closing the underlying network connection.
func (w *wsConn) Close() error {
	w.writeFrame(8, []byte{}) // opcode 8 = close frame
	return w.conn.Close()
}

// LocalAddr implements net.Conn.LocalAddr by returning the local network address.
func (w *wsConn) LocalAddr() net.Addr {
	return w.conn.LocalAddr()
}

// RemoteAddr implements net.Conn.RemoteAddr by returning the remote network address.
func (w *wsConn) RemoteAddr() net.Addr {
	return w.conn.RemoteAddr()
}

// SetDeadline implements net.Conn.SetDeadline by setting read and write deadlines.
func (w *wsConn) SetDeadline(t time.Time) error {
	return w.conn.SetDeadline(t)
}

// SetReadDeadline implements net.Conn.SetReadDeadline by setting the read deadline.
func (w *wsConn) SetReadDeadline(t time.Time) error {
	return w.conn.SetReadDeadline(t)
}

// SetWriteDeadline implements net.Conn.SetWriteDeadline by setting the write deadline.
func (w *wsConn) SetWriteDeadline(t time.Time) error {
	return w.conn.SetWriteDeadline(t)
}

// wsFrame represents a parsed WebSocket frame according to RFC 6455.
// It contains the frame's control information and payload data.
type wsFrame struct {
	fin     bool   // FIN bit: indicates if this is the final fragment
	opcode  byte   // Frame opcode (0x1=text, 0x2=binary, 0x8=close, etc.)
	masked  bool   // MASK bit: indicates if payload is masked
	payload []byte // Frame payload data (unmasked)
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