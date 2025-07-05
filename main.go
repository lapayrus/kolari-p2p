package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		allowedOrigin := os.Getenv("ALLOWED_ORIGIN")
		if allowedOrigin == "" {
			allowedOrigin = "http://localhost:8080" 
		}
		return r.Header.Get("Origin") == allowedOrigin
	},
}

type Client struct {
	conn                *websocket.Conn
	send                chan WebSocketMessage
	currentFileMetadata map[string]interface{}
	closeOnce           sync.Once
}

type WebSocketMessage struct {
	Type int // websocket.TextMessage or websocket.BinaryMessage
	Data []byte
}

type Message struct {
	Type     int // websocket.TextMessage or websocket.BinaryMessage
	Data     []byte
	Sender   *Client
	Metadata map[string]interface{} 
}

type Room struct {
	clients    map[*Client]bool
	broadcast  chan Message
	register   chan *Client
	unregister chan *Client
}

var rooms = make(map[string]*Room)
var roomsMutex = sync.Mutex{}

func (r *Room) run() {
	for {
		select {
		case client := <-r.register:
			r.clients[client] = true
			if len(r.clients) > 1 {
				for c := range r.clients {
					select {
					case c.send <- WebSocketMessage{Type: websocket.TextMessage, Data: []byte(`{"type": "status", "message": "User has joined. You can now share files.", "ready": true}`)}:
					default:
						close(c.send)
						delete(r.clients, c)
					}
				}
			}
		case client := <-r.unregister:
			if _, ok := r.clients[client]; ok {
				delete(r.clients, client)
				close(client.send)
			}
		case message := <-r.broadcast:
			for client := range r.clients {
				if client == message.Sender {
					continue 
				}

				// Add isSender flag to the message metadata if it's a file
				var dataToSend []byte
				msgType := message.Type

				if message.Type == websocket.BinaryMessage {
					
					metadata := message.Metadata
					metadata["isSender"] = false 
					if metadataJSON, err := json.Marshal(metadata); err == nil {
						select {
						case client.send <- WebSocketMessage{Type: websocket.TextMessage, Data: metadataJSON}:
						default:
					log.Printf("Error sending metadata to client: %v", err)
					client.closeOnce.Do(func() { close(client.send) })
					delete(r.clients, client)
					continue
						}
					}
					dataToSend = message.Data
				} else {
					
					var msg map[string]interface{}
					if err := json.Unmarshal(message.Data, &msg); err == nil {
						msg["isSender"] = false 
						if newData, err := json.Marshal(msg); err == nil {
							dataToSend = newData
						} else {
							log.Printf("Error marshaling text message: %v", err)
							continue
						}
					} else {
						log.Printf("Error unmarshaling text message: %v", err)
						continue
					}
				}

				select {
				case client.send <- WebSocketMessage{Type: msgType, Data: dataToSend}:
				default:
					log.Printf("Client %v buffer full, disconnecting", client)
					close(client.send)
					delete(r.clients, client)
				}
			}
		}
	}
}

func serveWs(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	roomId := vars["roomId"]

	roomsMutex.Lock()
	room, ok := rooms[roomId]
	if !ok {
		roomsMutex.Unlock()
		return
	}
	roomsMutex.Unlock()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	client := &Client{conn: conn, send: make(chan WebSocketMessage, 256), currentFileMetadata: make(map[string]interface{})}
	room.register <- client

	go client.writePump()
	go client.readPump(room)
}

func (c *Client) readPump(room *Room) {
	defer func() {
		room.unregister <- c
		c.conn.Close()
		c.closeOnce.Do(func() { close(c.send) })
	}()
	
	c.conn.SetReadLimit(500 * 1024 * 1024)

	for {
		messageType, p, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("client disconnected: %v", err)
			} else {
				log.Printf("read error: %v", err)
			}
			break
		}

		if messageType == websocket.TextMessage {
			var msg map[string]interface{}
			if err := json.Unmarshal(p, &msg); err != nil {
				log.Printf("unmarshal text message error: %v", err)
				continue
			}
			if msg["type"] == "file_metadata" {
				c.currentFileMetadata = msg
			} else {
				
				room.broadcast <- Message{Type: websocket.TextMessage, Data: p, Sender: c}
			}
		} else if messageType == websocket.BinaryMessage {
			if c.currentFileMetadata != nil {
				
				room.broadcast <- Message{Type: websocket.BinaryMessage, Data: p, Sender: c, Metadata: c.currentFileMetadata}
				c.currentFileMetadata = nil 
			} else {
				log.Println("Received binary message without preceding metadata")
			}
		}
	}
}

func (c *Client) writePump() {
	defer func() {
		c.conn.Close()
	}()
	for {
		select {
		case wsMsg, ok := <-c.send:
			if !ok {
				
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			err := c.conn.WriteMessage(wsMsg.Type, wsMsg.Data)
			if err != nil {
				log.Printf("write error: %v", err)
				return
			}
		}
	}
}

func serveHome(w http.ResponseWriter, r *http.Request) {
	roomId := uuid.New().String()[:8]
	http.Redirect(w, r, "/"+roomId, http.StatusFound)
}

func serveRoom(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	roomId := vars["roomId"]

	roomsMutex.Lock()
	if _, ok := rooms[roomId]; !ok {
		rooms[roomId] = &Room{
			broadcast:  make(chan Message),
			register:   make(chan *Client),
			unregister: make(chan *Client),
			clients:    make(map[*Client]bool),
		}
		go rooms[roomId].run()
	}
	roomsMutex.Unlock()

	tmpl, err := template.ParseFiles("templates/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl.Execute(w, nil)
}

func main() {
	r := mux.NewRouter()

	fs := http.FileServer(http.Dir("./static/"))
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", fs))

	r.HandleFunc("/", serveHome)
	r.HandleFunc("/{roomId}", serveRoom)
	r.HandleFunc("/ws/{roomId}", serveWs)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Printf("Starting server on :%s\n", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatal(err)
	}
}
