package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"runeshell/internal/agent"
	"runeshell/internal/termserver"
)

func main() {
	hubURL := flag.String("hub", "ws://localhost:8081/ws/agent", "hub agent ws url")
	agentID := flag.String("agent-id", "agent1", "agent id")
	agentSecret := flag.String("agent-secret", "agent-secret", "agent secret")
	keepSessions := flag.Bool("keep-sessions", false, "do not kill tmux sessions on disconnect")
	flag.Parse()

	manager := &termserver.LocalSessionManager{KillOnClose: !*keepSessions}
	client := &agent.Client{
		HubURL:  *hubURL,
		AgentID: *agentID,
		Secret:  *agentSecret,
		Manager: manager,
		Logger:  log.New(os.Stdout, "[agentd] ", log.LstdFlags),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := agent.RunWithRetry(ctx, client, 2); err != nil {
		log.Fatal(err)
	}
}
