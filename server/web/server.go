package web

import (
	"bytes"
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"server/api"
	"server/metrics"
	"server/storage"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//go:embed templates/*
var templateFiles embed.FS

//go:embed static/*
var staticFiles embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// wsConn wraps a WebSocket connection with a mutex to serialize reads and writes
type wsConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// writeJSON safely writes JSON to the connection with mutex protection
func (w *wsConn) writeJSON(v interface{}) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Set a write deadline to prevent blocking indefinitely
	w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return w.conn.WriteJSON(v)
}

// writeControl safely writes control messages to the connection with mutex protection
func (w *wsConn) writeControl(messageType int, data []byte, deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteControl(messageType, data, deadline)
}

// readMessage safely reads messages from the connection with mutex protection
func (w *wsConn) readMessage() (messageType int, p []byte, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.ReadMessage()
}

// close safely closes the connection with mutex protection
func (w *wsConn) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.Close()
}

// WebSocket connection manager
type wsConnectionManager struct {
	sync.RWMutex
	connections map[*wsConn]bool
}

func newWSConnectionManager() *wsConnectionManager {
	return &wsConnectionManager{
		connections: make(map[*wsConn]bool),
	}
}

func (m *wsConnectionManager) add(conn *websocket.Conn) *wsConn {
	m.Lock()
	defer m.Unlock()
	wrapped := &wsConn{conn: conn}
	m.connections[wrapped] = true
	return wrapped
}

func (m *wsConnectionManager) count() int {
	m.RLock()
	defer m.RUnlock()
	return len(m.connections)
}

func (m *wsConnectionManager) remove(wrapped *wsConn) {
	m.Lock()
	defer m.Unlock()
	delete(m.connections, wrapped)
}

func (m *wsConnectionManager) broadcast(update interface{}) {
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		metrics.WebSocketBroadcastDuration.Observe(duration)
	}()

	// Create a snapshot of connections while holding the read lock
	m.RLock()
	conns := make([]*wsConn, 0, len(m.connections))
	for conn := range m.connections {
		conns = append(conns, conn)
	}
	m.RUnlock()

	if len(conns) == 0 {
		slog.Warn("No WebSocket connections to broadcast to - message will be lost")
		return
	}

	slog.Debug("Broadcasting to WebSocket connections", "count", len(conns))

	// Now iterate over the snapshot without holding any lock
	// This allows goroutines to safely call remove() which acquires a write lock
	for _, wrapped := range conns {
		// Write in a non-blocking goroutine
		go func(c *wsConn) {
			if err := c.writeJSON(update); err != nil {
				slog.Debug("Failed to send WebSocket message to client", "error", err)
				c.close()
				m.remove(c)
			}
		}(wrapped)
	}

	metrics.WebSocketMessagesBroadcast.Inc()
}

func StartWebServer(store storage.Storage, port string, version string) {
	r := mux.NewRouter()
	connManager := newWSConnectionManager()

	// Apply Prometheus middleware to all routes
	r.Use(prometheusMiddleware)

	// Prometheus metrics endpoint (before other routes to avoid middleware)
	r.Handle("/metrics", promhttp.Handler()).Methods("GET")

	// Serve static files (JS, CSS) from embedded filesystem
	// The embedded FS includes the "static/" prefix, so we need to use a subdirectory
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		slog.Error("Failed to create static files subdirectory", "error", err)
		os.Exit(1)
	}
	// Serve static files with cache-busting headers for development
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
	r.PathPrefix("/static/").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Add headers to prevent caching during development
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		staticHandler.ServeHTTP(w, r)
	}))

	// API routes
	r.HandleFunc("/api/systems", api.GetSystemsHandler(store)).Methods("GET")
	r.HandleFunc("/api/systems/{hostname}", api.GetSystemHandler(store)).Methods("GET")
	r.HandleFunc("/api/systems/{hostname}", api.DeleteSystemHandler(store)).Methods("DELETE")

	// API documentation endpoint
	r.HandleFunc("/apidoc", apiDocsHandler(version))

	// Serve the main page from embedded filesystem
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, err := templateFiles.ReadFile("templates/index.html")
		if err != nil {
			slog.Error("Failed to read index.html", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		data = bytes.ReplaceAll(data, []byte("{{VERSION}}"), []byte(version))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	// WebSocket endpoint
	r.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("Failed to upgrade WebSocket connection", "error", err)
			return
		}

		// Configure WebSocket connection
		conn.SetReadLimit(512) // Limit size of incoming messages
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		// Add connection to manager and get wrapped connection
		wrappedConn := connManager.add(conn)
		metrics.WebSocketConnections.Inc() // Increment on connect
		slog.Debug("New WebSocket connection established", "remote_addr", conn.RemoteAddr())

		// Create a context for the ping ticker to allow graceful shutdown
		pingCtx, pingCancel := context.WithCancel(context.Background())
		defer pingCancel()

		// Start ping ticker
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-pingCtx.Done():
					// Connection closed, stop the ticker
					return
				case <-ticker.C:
					// Use wrapped connection's writeControl which is thread-safe
					if err := wrappedConn.writeControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
						slog.Debug("Failed to send ping to client", "error", err)
						return
					}
				}
			}
		}()

		// Cleanup on exit
		defer func() {
			slog.Debug("WebSocket connection closing")
			pingCancel()                       // Signal ping ticker to stop
			metrics.WebSocketConnections.Dec() // Decrement on disconnect
			connManager.remove(wrappedConn)
			wrappedConn.close()
		}()

		// Keep connection alive and handle incoming messages
		// Note: We call conn.ReadMessage() directly here (not wrappedConn.readMessage())
		// because ReadMessage blocks, and locking during a blocking call would prevent writes.
		// This is safe because only this single goroutine calls ReadMessage.
		for {
			messageType, p, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					slog.Debug("WebSocket read error", "error", err)
				}
				break
			}
			// Handle control frame ping messages
			if messageType == websocket.PingMessage {
				// Use wrapped connection's writeControl which is thread-safe
				if err := wrappedConn.writeControl(websocket.PongMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
					slog.Debug("Failed to send pong to client", "error", err)
					break
				}
			}
			// Handle text message "ping" from client (client sends 'ping' as text for keepalive)
			if messageType == websocket.TextMessage && string(p) == "ping" {
				// Client is just checking connection, no response needed
				// The ping/pong control frames handle the actual keepalive
				continue
			}
		}
	})

	// Subscribe to system updates and broadcast to all WebSocket clients
	go func() {
		for update := range store.SubscribeToUpdates() {
			connCount := connManager.count()
			if connCount > 0 {
				slog.Debug("Broadcasting system update via WebSocket", "hostname", update.Hostname, "connections", connCount)
				connManager.broadcast(update)
			}
		}
		slog.Warn("Updates channel closed, WebSocket broadcast goroutine exiting")
	}()

	slog.Info("Web server starting", "port", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		slog.Error("Web server failed", "error", err)
	}
}

// apiDocsHandler serves the API documentation page
func apiDocsHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := templateFiles.ReadFile("templates/apidoc.html")
		if err != nil {
			slog.Error("Failed to read apidoc.html", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		data = bytes.ReplaceAll(data, []byte("{{VERSION}}"), []byte(version))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}
}
