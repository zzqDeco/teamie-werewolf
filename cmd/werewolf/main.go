package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zzqDeco/teamie-werewolf/internal/auth"
	"github.com/zzqDeco/teamie-werewolf/internal/game"
	"github.com/zzqDeco/teamie-werewolf/internal/httpapi"
	webui "github.com/zzqDeco/teamie-werewolf/web"
)

var (
	buildVersion = "0.1.0"
	buildSHA     = "dev"
)

func main() {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}

	devSimulator := strings.EqualFold(strings.TrimSpace(os.Getenv("DEV_SIMULATOR")), "true")
	appID := strings.TrimSpace(os.Getenv("TEAMIE_APP_ID"))
	signingSecret := strings.TrimSpace(os.Getenv("TEAMIE_APP_SIGNING_SECRET"))

	if devSimulator {
		if appID == "" {
			appID = "00000000-0000-4000-8000-devsimulator1"
		}
		if signingSecret == "" {
			signingSecret = "tmsec_local_dev_simulator_secret_only"
		}
	}

	verifier, err := auth.NewVerifier(appID, signingSecret)
	if err != nil {
		log.Fatalf("fatal: invalid Teamie app credentials: %v", err)
	}

	timings := game.DefaultTimings()
	if rawSec := strings.TrimSpace(os.Getenv("PHASE_TIMEOUT_SEC")); rawSec != "" {
		if sec, convErr := strconv.Atoi(rawSec); convErr == nil && sec >= 5 && sec <= 300 {
			d := time.Duration(sec) * time.Second
			timings.NightTimeout = d
			timings.SpeakerTurnTimeout = d
			timings.VoteTimeout = d
		}
	}

	version := strings.TrimSpace(os.Getenv("APP_VERSION"))
	if version == "" {
		version = buildVersion
	}
	gitSHA := strings.TrimSpace(os.Getenv("GIT_SHA"))
	if gitSHA == "" {
		gitSHA = buildSHA
	}

	teamieOrigin := strings.TrimSpace(os.Getenv("TEAMIE_ORIGIN"))
	if teamieOrigin == "" {
		teamieOrigin = "https://120.25.210.202"
	}

	staticFS, err := webui.DistFS()
	if err != nil {
		log.Fatalf("fatal: failed to load embedded web UI dist: %v", err)
	}

	mgr := game.NewManager(timings)
	appSrv := httpapi.NewServer(httpapi.Config{
		AppID:         appID,
		SigningSecret: signingSecret,
		TeamieOrigin:  teamieOrigin,
		Version:       version,
		GitSHA:        gitSHA,
		DevSimulator:  devSimulator,
		StaticFS:      staticFS,
	}, verifier, mgr)

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           appSrv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf(
			"teamie-werewolf listening on :%s (version=%s sha=%s appID=%s teamieOrigin=%s devSimulator=%v)",
			port, version, gitSHA, appID, teamieOrigin, devSimulator,
		)
		if serveErr := httpSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Fatalf("http server error: %v", serveErr)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down teamie-werewolf...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	appSrv.Close()
}
