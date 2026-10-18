package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"igcrm/internal/config"
	"igcrm/internal/extract"
	"igcrm/internal/ig"
	"igcrm/internal/jobs"
	"igcrm/internal/notify"
	"igcrm/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if err := run(cmd, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cmd string, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	if err := st.SeedToken(ctx, cfg.IGAccessToken, cfg.IGForceToken); err != nil {
		return err
	}

	igc := &ig.Client{
		Version: cfg.IGAPIVersion,
		Token: func(ctx context.Context) (string, error) {
			t, err := st.Token(ctx)
			return t.AccessToken, err
		},
	}

	switch cmd {
	case "migrate":
		log.Info("migrations applied")
		return nil
	case "backfill":
		return jobs.Backfill(ctx, st, igc, cfg.ExtractDebounce, log)
	case "serve":
		return serve(ctx, cfg, st, igc, log)
	default:
		return fmt.Errorf("unknown command %q (serve | backfill | migrate)", cmd)
	}
}

func serve(ctx context.Context, cfg config.Config, st *store.Store, igc *ig.Client, log *slog.Logger) error {
	n := &notify.Notifier{BotToken: cfg.TGBotToken, ChatID: cfg.TGChatID, Log: log}

	go (&jobs.TokenRefresher{Store: st, IG: igc, Notify: n, Threshold: cfg.IGRefreshThreshold, Log: log}).Run(ctx)
	go (&jobs.ProfileFetcher{Store: st, IG: igc, Log: log}).Run(ctx)
	if cfg.LLMEnabled() {
		var llm extract.Completer
		if cfg.LLMProvider == "claude" {
			llm = extract.NewClaude(cfg.LLMAPIKey, cfg.LLMModel)
		} else {
			llm = &extract.LLM{BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey, Model: cfg.LLMModel}
		}
		go (&extract.Worker{Store: st, LLM: llm, Model: cfg.LLMModel, History: cfg.ExtractHistory, Log: log, Notify: n}).Run(ctx)
		log.Info("extraction enabled", "provider", cfg.LLMProvider, "model", cfg.LLMModel)
	} else {
		log.Warn("extraction disabled: задай LLM_MODEL і LLM_API_KEY (claude) або LLM_BASE_URL (openai-сумісний)")
	}

	mux := http.NewServeMux()
	mux.Handle("/ig/webhook", &ig.WebhookHandler{
		AppSecret:   cfg.IGAppSecret,
		VerifyToken: cfg.IGVerifyToken,
		Store:       st,
		Debounce:    cfg.ExtractDebounce,
		Log:         log,
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Info("listening", "addr", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
