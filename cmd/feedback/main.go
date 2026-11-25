// Command feedback is the central feedback receiver: installations post feedback, it files GitHub issues.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/feedback"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

func main() {
	telemetry.InitLogger("eduide-feedback")
	if err := run(); err != nil {
		slog.Error("feedback failed", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("listen", ":8080", "listen address")
	keysDir := flag.String("keys-dir", "/etc/feedback/keys", "directory with one file per installation (name = installation, content = key)")
	apiURL := flag.String("github-api-url", "https://api.github.com", "GitHub API base URL")
	appID := flag.String("github-app-id", "", "GitHub App id")
	installID := flag.String("github-installation-id", "", "GitHub App installation id")
	repo := flag.String("github-repository", "", "triage repository, owner/name")
	keyFile := flag.String("github-private-key", "/etc/feedback/github/private-key.pem", "GitHub App private key (PEM)")
	flag.Parse()

	if *appID == "" || *installID == "" || *repo == "" {
		return errors.New("github-app-id, github-installation-id and github-repository are required")
	}
	pem, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	key, err := feedback.ParseKey(pem)
	if err != nil {
		return err
	}
	s := &feedback.Server{
		KeysDir: *keysDir,
		GitHub:  &feedback.GitHub{APIURL: *apiURL, AppID: *appID, InstallationID: *installID, Repository: *repo, Key: key},
	}
	if err := s.Check(); err != nil {
		return err
	}
	srv := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("feedback receiver listening", "addr", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
