package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"

	"runeshell/internal/hub"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	staticDir := flag.String("static", "web", "static web directory")
	tokenSecret := flag.String("token-secret", "dev-secret", "token signing secret")
	adminToken := flag.String("admin-token", "dev-admin", "admin token for ws-token issuance")
	agentID := flag.String("agent-id", "agent1", "agent id")
	agentSecret := flag.String("agent-secret", "agent-secret", "agent secret")
	tokenTTL := flag.Int("token-ttl", 60, "token ttl seconds")
	authMode := flag.String("auth-mode", "token", "auth mode: token or tailnet")
	tailnetOnly := flag.Bool("tailnet-only", false, "allow only tailnet IPs (100.64.0.0/10)")
	flag.Parse()

	mgr := hub.NewTokenManager(*tokenSecret)
	agentSecrets := map[string]string{*agentID: *agentSecret}
	h := hub.NewHub(mgr, agentSecrets)
	h.AuthMode = *authMode
	h.TailnetOnly = *tailnetOnly
	h.SetLogger(log.New(os.Stdout, "[hubd] ", log.LstdFlags))

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/client", h.ServeClientWS)
	mux.HandleFunc("/ws/agent", h.ServeAgentWS)
	if *authMode == hub.AuthModeToken {
		mux.HandleFunc("/api/ws-token", h.TokenHandler(*adminToken, *tokenTTL))
	}
	mux.HandleFunc("/api/lock", h.LockHandler(*adminToken))
	mux.HandleFunc("/api/sessions", h.SessionsHandler(*adminToken))
	mux.Handle("/", noCacheFiles(*staticDir))

	log.Printf("hubd listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func noCacheFiles(staticDir string) http.Handler {
	fs := http.FileServer(http.Dir(staticDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "" || path == "/" || strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") {
			w.Header().Set("Cache-Control", "no-store")
		}
		fs.ServeHTTP(w, r)
	})
}
