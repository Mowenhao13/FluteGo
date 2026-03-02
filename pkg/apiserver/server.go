package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Allow all origins for local / LAN use.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Server is the HTTP + WebSocket API server.
type Server struct {
	port     int
	mode     string // "sender" | "receiver"
	destIP   string
	filePort int
	state    *StateStore
	hub      *Hub

	sendFn       func(fileName string, data io.Reader, fecType string) (uint8, error)
	setSaveDirFn func(dir string) error
	setDestFn    func(ip string) error
	uploadDir    string
	staticContent []byte
	browseDirFn   func() (string, error)

	mu        sync.RWMutex
	recvReady bool
	saveDir   string
}

// New creates a new Server with the given port and mode ("sender"|"receiver").
func New(port int, mode string) *Server {
	hub := NewHub()
	return &Server{
		port:     port,
		mode:     mode,
		filePort: 3400, // default; override with WithFilePort
		hub:      hub,
		state:    newStateStore(hub),
	}
}

// WithDestIP sets the destination IP address returned by /api/status.
func (s *Server) WithDestIP(ip string) *Server {
	s.destIP = ip
	return s
}

// WithFilePort sets the base file port returned by /api/status.
func (s *Server) WithFilePort(port int) *Server {
	s.filePort = port
	return s
}

// SetSendFunc registers the function called by POST /api/send.
func (s *Server) SetSendFunc(fn func(string, io.Reader, string) (uint8, error)) *Server {
	s.sendFn = fn
	return s
}

// WithUploadDir sets the server-side directory for uploaded files.
func (s *Server) WithUploadDir(dir string) *Server {
	s.uploadDir = dir
	return s
}

// SetSaveDirFunc registers the function called by POST /api/receive/set-dir.
func (s *Server) SetSaveDirFunc(fn func(string) error) *Server {
	s.setSaveDirFn = fn
	return s
}

// SetDestFunc registers the function called by POST /api/sender/set-dest.
func (s *Server) SetDestFunc(fn func(string) error) *Server {
	s.setDestFn = fn
	return s
}

// WithStaticContent sets the HTML content served at GET /.
func (s *Server) WithStaticContent(content []byte) *Server {
	s.staticContent = content
	return s
}

// SetBrowseDirFunc registers the function called by POST /api/receiver/browse-dir.
func (s *Server) SetBrowseDirFunc(fn func() (string, error)) *Server {
	s.browseDirFn = fn
	return s
}

// SetReceiverReady sets whether the receiver subsystem is ready.
func (s *Server) SetReceiverReady(v bool) {
	s.mu.Lock()
	s.recvReady = v
	s.mu.Unlock()
}

// WithSaveDir sets the initial save directory shown in /api/status.
func (s *Server) WithSaveDir(dir string) *Server {
	s.mu.Lock()
	s.saveDir = dir
	s.mu.Unlock()
	return s
}

// State returns the StateStore used to register and update transfer records.
func (s *Server) State() *StateStore {
	return s.state
}

// withCORS wraps an HTTP handler to add CORS headers for file:// and cross-origin access.
func withCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h(w, r)
	}
}

// Start registers HTTP routes and serves until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	go s.hub.Run()

	mux := http.NewServeMux()
	if len(s.staticContent) > 0 {
		mux.HandleFunc("/", s.handleStatic)
	}
	mux.HandleFunc("/api/status",                  withCORS(s.handleStatus))
	mux.HandleFunc("/api/transfers",               withCORS(s.handleTransfers))
	mux.HandleFunc("/api/send",                    withCORS(s.handleSend))
	mux.HandleFunc("/api/receive/set-dir",         withCORS(s.handleSetDir))
	mux.HandleFunc("/api/sender/set-dest",         withCORS(s.handleSetDest))
	mux.HandleFunc("/api/receiver/browse-dir",     withCORS(s.handleBrowseDir))
	mux.HandleFunc("/ws", s.handleWS) // WS does not need CORS header

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background()) //nolint:errcheck
	}()

	log.Printf("[apiserver] listening on :%d (mode=%s)", s.port, s.mode)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	recvReady := s.recvReady
	saveDir := s.saveDir
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
		"mode":          s.mode,
		"destIP":        s.destIP,
		"filePort":      s.filePort,
		"apiVersion":    "1",
		"receiverReady": recvReady,
		"saveDir":       saveDir,
	})
}

func (s *Server) handleTransfers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.state.Snapshot()) //nolint:errcheck
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.sendFn == nil {
		http.Error(w, `{"error":"send not available in this mode"}`, http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(512 << 20); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "missing file field: " + err.Error()}) //nolint:errcheck
		return
	}
	defer file.Close()

	fecType := r.FormValue("fecType")
	if fecType == "" {
		fecType = "NoCode"
	}

	fdtID, err := s.sendFn(header.Filename, file, fecType)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
		"fdtId":    fdtID,
		"fileName": header.Filename,
	})
}

func (s *Server) handleSetDir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.setSaveDirFn == nil {
		http.Error(w, `{"error":"set-dir not available in this mode"}`, http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		SaveDir string `json:"saveDir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON: " + err.Error()}) //nolint:errcheck
		return
	}
	if body.SaveDir == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "saveDir is required"}) //nolint:errcheck
		return
	}

	if err := s.setSaveDirFn(body.SaveDir); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
		return
	}

	// Update the cached saveDir for /api/status
	s.mu.Lock()
	s.saveDir = body.SaveDir
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"saveDir": body.SaveDir}) //nolint:errcheck
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.staticContent) //nolint:errcheck
}

func (s *Server) handleBrowseDir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.browseDirFn == nil {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	dir, err := s.browseDirFn()
	if err != nil {
		// zenity.ErrCanceled or any error → 204 (no selection)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"dir": dir}) //nolint:errcheck
}

func (s *Server) handleSetDest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.setDestFn == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "set-dest not available in this mode"}) //nolint:errcheck
		return
	}

	var body struct {
		DestIP string `json:"destIP"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON: " + err.Error()}) //nolint:errcheck
		return
	}
	if body.DestIP == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "destIP is required"}) //nolint:errcheck
		return
	}

	if err := s.setDestFn(body.DestIP); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"destIP": body.DestIP}) //nolint:errcheck
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[apiserver] ws upgrade failed: %v", err)
		return
	}

	client := &Client{
		hub:  s.hub,
		conn: conn,
		send: make(chan []byte, 256),
	}
	s.hub.register <- client

	// Send full snapshot immediately on connect.
	if msg := encodeWSMsg("snapshot", s.state.Snapshot()); msg != nil {
		client.send <- msg
	}

	go client.writePump()

	// Read loop: drain incoming frames and detect disconnection.
	go func() {
		defer func() {
			s.hub.unregister <- client
			conn.Close()
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
}
