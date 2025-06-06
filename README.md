# websocks
Super Simple websocket client and server/handler in Go

## API

### Client
```go
func Connect(ctx context.Context, url string) (net.Conn, error)
```

### Server
```go
type AcceptFunc func(conn net.Conn) error
type Server struct { *http.Server }

func NewServer(addr, path string, acceptFunc AcceptFunc) *Server
func (s *Server) Start() error
```

### Handler
```go
func NewHandler(acceptFunc AcceptFunc) http.HandlerFunc
```

## Example Usage

```go
import v1 "github.com/catalystcommunity/websocks/v1"

// Client
conn, err := v1.Connect(ctx, "ws://localhost:8080/ws")
conn.Write([]byte("hello"))

// Server
server := v1.NewServer(":8080", "/ws", func(conn net.Conn) error {
    // Handle connection like TCP socket
    return nil
})
server.Start()

// Handler with existing mux
handler := v1.NewHandler(acceptFunc)
http.Handle("/ws", handler)
```
