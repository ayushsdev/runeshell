package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"runeshell/internal/agent"
	"runeshell/internal/hub"
	"runeshell/internal/qr"
	"runeshell/internal/termserver"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "hub":
		hubCmd(os.Args[2:])
	case "agent":
		agentCmd(os.Args[2:])
	case "attach":
		attachCmd(os.Args[2:])
	case "lock":
		lockCmd(os.Args[2:], "none")
	case "unlock":
		lockCmd(os.Args[2:], "web")
	case "qr":
		qrCmd(os.Args[2:])
	case "version", "--version", "-version":
		fmt.Printf("runeshell %s (%s) %s\n", version, commit, date)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("runeshell <command> [args]")
	fmt.Println("")
	fmt.Println("Commands:")
	fmt.Println("  run      Start hub + agent together")
	fmt.Println("  hub      Start hub only")
	fmt.Println("  agent    Start agent only")
	fmt.Println("  attach   Attach local tmux session")
	fmt.Println("  lock     Disable web input (admin token required)")
	fmt.Println("  unlock   Re-enable web input (admin token required)")
	fmt.Println("  qr       Print QR code for a URL")
	fmt.Println("  version  Print version")
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8081", "hub listen address")
	staticDir := fs.String("static", "web", "static web directory")
	authMode := fs.String("auth-mode", hub.AuthModeTailnet, "auth mode: token or tailnet")
	tailnetOnly := fs.Bool("tailnet-only", false, "allow only tailnet IPs (100.64.0.0/10)")
	tokenSecret := fs.String("token-secret", "dev-secret", "token signing secret")
	adminToken := fs.String("admin-token", "dev-admin", "admin token for lock/token endpoints")
	tokenTTL := fs.Int("token-ttl", 60, "token ttl seconds (token mode)")
	agentID := fs.String("agent-id", "agent1", "agent id")
	agentSecret := fs.String("agent-secret", "agent-secret", "agent secret")
	session := fs.String("session", "ai", "default session name")
	baseURL := fs.String("url", "", "base URL to print/QR (e.g. https://host)")
	printQR := fs.Bool("qr", true, "print QR code to terminal")
	keepSessions := fs.Bool("keep-sessions", false, "do not kill tmux sessions on disconnect")
	fs.Parse(args)

	if *authMode == hub.AuthModeTailnet && *baseURL == "" {
		dnsName, err := ensureTailnetServe(*addr)
		if err != nil {
			log.Fatalf("tailnet setup failed: %v", err)
		}
		*baseURL = "https://" + dnsName
	}

	server, err := startHub(*addr, *staticDir, *authMode, *tailnetOnly, *tokenSecret, *adminToken, *tokenTTL, *agentID, *agentSecret)
	if err != nil {
		log.Fatalf("hub: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	agentURL := wsURL(*addr, "/ws/agent")
	manager := &termserver.LocalSessionManager{KillOnClose: !*keepSessions}
	client := &agent.Client{
		HubURL:  agentURL,
		AgentID: *agentID,
		Secret:  *agentSecret,
		Manager: manager,
		Logger:  log.New(os.Stdout, "[agent] ", log.LstdFlags),
	}
	go func() {
		if err := agent.RunWithRetry(ctx, client, 2*time.Second); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("agent stopped: %v", err)
		}
	}()

	share := buildShareURL(*baseURL, *addr, *agentID, *session)
	fmt.Printf("Open: %s\n", share)
	if *printQR {
		_ = qr.RenderANSI(os.Stdout, share)
		fmt.Println()
	}

	<-ctx.Done()
	_ = server.Shutdown(context.Background())
}

func hubCmd(args []string) {
	fs := flag.NewFlagSet("hub", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8081", "hub listen address")
	staticDir := fs.String("static", "web", "static web directory")
	authMode := fs.String("auth-mode", hub.AuthModeTailnet, "auth mode: token or tailnet")
	tailnetOnly := fs.Bool("tailnet-only", false, "allow only tailnet IPs (100.64.0.0/10)")
	tokenSecret := fs.String("token-secret", "dev-secret", "token signing secret")
	adminToken := fs.String("admin-token", "dev-admin", "admin token for lock/token endpoints")
	tokenTTL := fs.Int("token-ttl", 60, "token ttl seconds (token mode)")
	agentID := fs.String("agent-id", "agent1", "agent id")
	agentSecret := fs.String("agent-secret", "agent-secret", "agent secret")
	fs.Parse(args)

	server, err := startHub(*addr, *staticDir, *authMode, *tailnetOnly, *tokenSecret, *adminToken, *tokenTTL, *agentID, *agentSecret)
	if err != nil {
		log.Fatalf("hub: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	_ = server.Shutdown(context.Background())
}

func agentCmd(args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	hubURL := fs.String("hub", "ws://127.0.0.1:8081/ws/agent", "hub agent ws url")
	agentID := fs.String("agent-id", "agent1", "agent id")
	agentSecret := fs.String("agent-secret", "agent-secret", "agent secret")
	keepSessions := fs.Bool("keep-sessions", false, "do not kill tmux sessions on disconnect")
	fs.Parse(args)

	manager := &termserver.LocalSessionManager{KillOnClose: !*keepSessions}
	client := &agent.Client{
		HubURL:  *hubURL,
		AgentID: *agentID,
		Secret:  *agentSecret,
		Manager: manager,
		Logger:  log.New(os.Stdout, "[agent] ", log.LstdFlags),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := agent.RunWithRetry(ctx, client, 2*time.Second); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func attachCmd(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	session := fs.String("session", "ai", "tmux session name")
	fs.Parse(args)

	cmd := exec.Command("tmux", "new", "-As", *session)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}
}

func lockCmd(args []string, writer string) {
	fs := flag.NewFlagSet("lock", flag.ExitOnError)
	hubHTTP := fs.String("hub", "http://127.0.0.1:8081", "hub base url")
	adminToken := fs.String("admin-token", "dev-admin", "admin token")
	agentID := fs.String("agent-id", "agent1", "agent id")
	session := fs.String("session", "ai", "session id")
	fs.Parse(args)

	body, _ := json.Marshal(map[string]string{
		"agent_id":   *agentID,
		"session_id": *session,
		"writer":     writer,
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(*hubHTTP, "/")+"/api/lock", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+*adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		log.Fatalf("lock failed: %s", strings.TrimSpace(string(b)))
	}
}

func qrCmd(args []string) {
	fs := flag.NewFlagSet("qr", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() == 0 {
		log.Fatal("usage: runeshell qr <url>")
	}
	if err := qr.RenderANSI(os.Stdout, fs.Arg(0)); err != nil {
		log.Fatal(err)
	}
}

func startHub(addr, staticDir, authMode string, tailnetOnly bool, tokenSecret, adminToken string, tokenTTL int, agentID, agentSecret string) (*http.Server, error) {
	mgr := hub.NewTokenManager(tokenSecret)
	agentSecrets := map[string]string{agentID: agentSecret}
	h := hub.NewHub(mgr, agentSecrets)
	h.AuthMode = authMode
	h.TailnetOnly = tailnetOnly
	h.SetLogger(log.New(os.Stdout, "[hub] ", log.LstdFlags))

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/client", h.ServeClientWS)
	mux.HandleFunc("/ws/agent", h.ServeAgentWS)
	if authMode == hub.AuthModeToken {
		mux.HandleFunc("/api/ws-token", h.TokenHandler(adminToken, tokenTTL))
	}
	mux.HandleFunc("/api/lock", h.LockHandler(adminToken))
	mux.HandleFunc("/api/sessions", h.SessionsHandler(adminToken))
	mux.Handle("/", noCacheFiles(staticDir))

	server := &http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()
	return server, nil
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

func wsURL(addr, path string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.Replace(addr, "http://", "ws://", 1) + path
	}
	return "ws://" + addr + path
}

func buildShareURL(baseURL, addr, agentID, session string) string {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = "http://" + addr
	}
	return fmt.Sprintf("%s/?mode=hub&agent=%s&session=%s", base, agentID, session)
}

func ensureTailnetServe(addr string) (string, error) {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return "", errors.New("tailscale CLI not found; install and run `tailscale up`")
	}
	status, err := tailscaleStatus()
	if err != nil {
		return "", err
	}
	if status.BackendState != "Running" {
		return "", fmt.Errorf("tailscale not running (state=%s); run `tailscale up`", status.BackendState)
	}
	if status.SelfDNSName == "" {
		return "", errors.New("MagicDNS is not enabled; enable MagicDNS in the Tailscale admin console")
	}
	port := portFromAddr(addr)
	if port == "" {
		return "", fmt.Errorf("invalid addr: %s", addr)
	}
	cmd := exec.Command("tailscale", "serve", "--bg", "--yes", "--https", port)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("tailscale serve failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimRight(status.SelfDNSName, "."), nil
}

type tsStatus struct {
	BackendState string
	SelfDNSName  string
}

func tailscaleStatus() (tsStatus, error) {
	cmd := exec.Command("tailscale", "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return tsStatus{}, errors.New("failed to read tailscale status; is tailscale running?")
	}
	var raw struct {
		BackendState string `json:"BackendState"`
		Self         struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return tsStatus{}, fmt.Errorf("failed to parse tailscale status: %w", err)
	}
	return tsStatus{BackendState: raw.BackendState, SelfDNSName: raw.Self.DNSName}, nil
}

func portFromAddr(addr string) string {
	parts := strings.Split(addr, ":")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
