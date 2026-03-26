package apiserver

import (
	"encoding/json"
	"log"

	"github.com/gorilla/websocket"
)

// Client represents a single WebSocket connection managed by the Hub.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// Hub maintains the set of active WebSocket clients and broadcasts messages.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
}

// NewHub creates and returns a new Hub.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 4096),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub's dispatch loop. Call as a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
		case msg := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- msg:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

// Broadcast enqueues a message for delivery to all connected clients.
func (h *Hub) Broadcast(msg []byte) {
	select {
	case h.broadcast <- msg:
	default:
		log.Printf("[hub] broadcast channel full, dropping message")
	}
}

// wsMessage is the wire format for WebSocket messages.
type wsMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// encodeWSMsg serializes a WebSocket message envelope to JSON.
func encodeWSMsg(msgType string, data interface{}) []byte {
	b, err := json.Marshal(wsMessage{Type: msgType, Data: data})
	if err != nil {
		log.Printf("[hub] failed to marshal ws message: %v", err)
		return nil
	}
	return b
}

// writePump pumps messages from the hub send channel to the WebSocket connection.
func (c *Client) writePump() {
	defer c.conn.Close()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}
