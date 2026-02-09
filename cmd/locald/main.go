package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"runeshell/internal/termserver"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	staticDir := flag.String("static", "web", "static web directory")
	token := flag.String("token", "", "token required for WS connections (optional)")
	keepSessions := flag.Bool("keep-sessions", false, "do not kill tmux sessions on disconnect")
	flag.Parse()

	manager := &termserver.LocalSessionManager{KillOnClose: !*keepSessions}
	server := termserver.NewServer(manager, log.New(os.Stdout, "[locald] ", log.LstdFlags))
	if *token != "" {
		server.Authorizer = termserver.TokenAuthorizer(*token)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", server.ServeWS)
	mux.Handle("/", http.FileServer(http.Dir(*staticDir)))

	log.Printf("locald listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
