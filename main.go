package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/google/uuid"

	"github.com/gorilla/websocket"
)

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	sessions      = make(map[string]map[string]*websocket.Conn)
	sessionsMutex sync.Mutex
)

func generateSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionsMutex.Lock()
	defer sessionsMutex.Unlock()

	u := uuid.New().String()
	for {
		if _, ok := sessions[u]; !ok {
			break
		}
		u = uuid.New().String()
	}
	sessions[u] = make(map[string]*websocket.Conn)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"uuid": "%s"}`, u)
}

func sessionHandler(w http.ResponseWriter, r *http.Request) {
	uuid := r.URL.Path[1:]

	sessionsMutex.Lock()
	defer sessionsMutex.Unlock()

	if _, ok := sessions[uuid]; !ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	t, err := template.ParseFiles("public/session.html")
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		log.Printf("Error parsing session.html: %v", err)
		return
	}
	t.Execute(w, nil)
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, sErr := upgrader.Upgrade(w, r, nil)
	if sErr != nil {
		log.Println(sErr)
		return
	}
	defer conn.Close()

	sessionID := r.URL.Path[len("/ws/"):]
	peerID := r.URL.Query().Get("peerId")
	if peerID == "" {
		peerID = uuid.New().String()
	}

	sessionsMutex.Lock()
	var isReconnection bool
	if _, ok := sessions[sessionID]; !ok {
		sessionsMutex.Unlock()
		log.Printf("Attempted to connect to non-existent session: %s", sessionID)
		return
	}

	if existingConn, found := sessions[sessionID][peerID]; found {
		isReconnection = true
		log.Printf("Client %s (same peerID) reconnected to session %s. Closing old connection.", peerID, sessionID)
		existingConn.Close()
	}

	sessions[sessionID][peerID] = conn
	sessionsMutex.Unlock()

	if isReconnection {
		if mErr := conn.WriteJSON(map[string]interface{}{"type": "client-reconnected", "peerId": peerID, "userCount": len(sessions[sessionID])}); mErr != nil {
			log.Printf("Error sending client-reconnected message to %s: %v", peerID, mErr)
			conn.Close()
			delete(sessions[sessionID], peerID)
			return
		}
	}

	log.Printf("Client %s connected to session %s: %s", peerID, sessionID, conn.RemoteAddr().String())

	sessionsMutex.Lock()
	if lErr := conn.WriteJSON(map[string]interface{}{"type": "client-connected", "peerId": peerID}); lErr != nil {
		log.Printf("Error sending client-connected message to new client %s: %v", peerID, lErr)
		conn.Close()
		delete(sessions[sessionID], peerID)
		sessionsMutex.Unlock()
		return
	}

	for existingPeerID, existingConn := range sessions[sessionID] {
		if existingPeerID != peerID {
			if err := existingConn.WriteJSON(map[string]interface{}{"type": "new-peer", "peerId": peerID, "userCount": len(sessions[sessionID])}); err != nil {
				log.Printf("Error sending new-peer message to %s: %v", existingPeerID, err)
				existingConn.Close()
				delete(sessions[sessionID], existingPeerID)
			}
		}
	}
	sessionsMutex.Unlock()

	sessionsMutex.Lock()
	var existingPeerIDs []string
	for existingPeerID := range sessions[sessionID] {
		if existingPeerID != peerID {
			existingPeerIDs = append(existingPeerIDs, existingPeerID)
		}
	}
	if err := conn.WriteJSON(map[string]interface{}{"type": "existing-peers", "peerIds": existingPeerIDs, "userCount": len(sessions[sessionID])}); err != nil {
		log.Printf("Error sending existing-peers message to new client %s: %v", peerID, err)
		conn.Close()
		delete(sessions[sessionID], peerID)
		sessionsMutex.Unlock()
		return
	}
	sessionsMutex.Unlock()

	for {
		var msg map[string]interface{}
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("Error reading JSON message from %s: %v", conn.RemoteAddr().String(), err)
			break
		}

		log.Printf("Received message from %s in session %s: %+v", conn.RemoteAddr().String(), peerID, msg)

		msg["from"] = peerID

		sessionsMutex.Lock()
		if msgType, ok := msg["type"]; ok && (msgType == "offer" || msgType == "answer" || msgType == "ice-candidate") {
			if toPeerID, ok := msg["to"].(string); ok {
				if targetConn, found := sessions[sessionID][toPeerID]; found {
					if err := targetConn.WriteJSON(msg); err != nil {
						log.Printf("Error relaying WebRTC signaling message to client %s: %v", toPeerID, err)
						targetConn.Close()
						delete(sessions[sessionID], toPeerID)
					}
				} else {
					log.Printf("Target peer %s not found in session %s", toPeerID, sessionID)
				}
			} else {
				log.Printf("Invalid 'to' field in signaling message from %s", peerID)
			}
		} else if msgType, ok := msg["type"]; ok && msgType == "text-sync" {
			for pID, client := range sessions[sessionID] {
				if pID != peerID {
					if err := client.WriteJSON(msg); err != nil {
						log.Printf("Error writing text-sync message to client %s: %v", pID, err)
						client.Close()
						delete(sessions[sessionID], pID)
					}
				}
			}
		}

		if msgType, ok := msg["type"]; ok && msgType == "file-sent" {
			msg["type"] = "file-received"
			for pID, client := range sessions[sessionID] {
				if pID != peerID {
					if err := client.WriteJSON(msg); err != nil {
						log.Printf("Error writing file-received message to client %v: %v", pID, err)
						client.Close()
						delete(sessions[sessionID], pID)
					}
				}
			}
		}

		sessionsMutex.Unlock()
	}

	sessionsMutex.Lock()
	delete(sessions[sessionID], peerID)

	for existingPeerID, existingConn := range sessions[sessionID] {
		if existingPeerID != peerID {
			if err := existingConn.WriteJSON(map[string]interface{}{"type": "peer-disconnected", "peerId": peerID, "userCount": len(sessions[sessionID])}); err != nil {
				log.Printf("Error sending peer-disconnected message to %s: %v", existingPeerID, err)
				existingConn.Close()
				delete(sessions[sessionID], existingPeerID)
			}
		}
	}

	if len(sessions[sessionID]) == 0 {
		log.Printf("Session %s is empty, deleting.", sessionID)
		delete(sessions, sessionID)
	}
	sessionsMutex.Unlock()
	log.Printf("Client %s disconnected from session %s: %s", peerID, sessionID, conn.RemoteAddr().String())
}

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFile(w, r, "public/index.html")
			return
		}
		sessionHandler(w, r)
	})
	http.HandleFunc("/generate-session", generateSessionHandler)
	http.HandleFunc("/ws/", wsHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// var err error
	// if os.Getenv("APP_ENV") == "production" {
	// certFile := os.Getenv("TLS_CERT_FILE")
	// keyFile := os.Getenv("TLS_KEY_FILE")
	// if certFile == "" || keyFile == "" {
	// 	log.Fatal("TLS_CERT_FILE and TLS_KEY_FILE environment variables must be set for production environment")
	// }
	// err = http.ListenAndServeTLS(":"+port, certFile, keyFile, nil)
	// } else {
	// err = http.ListenAndServe(":"+port, nil)
	// }
	err := http.ListenAndServe(":"+port, nil)

	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
