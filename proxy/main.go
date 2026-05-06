package main

import (
	"flag"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func noCacheHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		h.ServeHTTP(w, r)
	})
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "missing target query parameter", http.StatusBadRequest)
		return
	}

	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("upgrade", "err", err)
		return
	}
	defer wsConn.Close()

	tcpConn, err := net.Dial("tcp", target)
	if err != nil {
		slog.Error("dial target", "target", target, "err", err)
		return
	}
	defer tcpConn.Close()

	slog.Info("proxying", "target", target, "remote", r.RemoteAddr)

	errc := make(chan error, 2)

	// WebSocket → TCP
	go func() {
		for {
			mt, data, err := wsConn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if mt == websocket.BinaryMessage || mt == websocket.TextMessage {
				if _, err := tcpConn.Write(data); err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	// TCP → WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := tcpConn.Read(buf)
			if n > 0 {
				if werr := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					errc <- werr
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					errc <- err
				} else {
					errc <- nil
				}
				return
			}
		}
	}()

	if err := <-errc; err != nil {
		slog.Debug("proxy done", "err", err)
	}
}

func main() {
	listen := flag.String("listen", ":8080", "listen address")
	static := flag.String("static", "static", "directory to serve static files from")
	flag.Parse()

	http.HandleFunc("/ws", handleWS)
	http.Handle("/", noCacheHandler(http.FileServer(http.Dir(*static))))

	log.Printf("Listening on %s (static: %s)", *listen, *static)
	if err := http.ListenAndServe(*listen, nil); err != nil {
		log.Fatal(err)
	}
}
