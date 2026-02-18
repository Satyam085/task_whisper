package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"taskwhisperer/bot"
	"taskwhisperer/config"
	"taskwhisperer/gemini"
	"taskwhisperer/scheduler"
	"taskwhisperer/store"
	"taskwhisperer/tasks"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	fmt.Println(`
 _____         _   _    _    _ _     _
|_   _|_ _ ___| |_| |  | |  | | |__ (_)___ _ __  ___ _ __ ___ _ __
  | |/ _` + "`" + ` / __| / / |  | |/\| | '_ \| / __| '_ \/ _ \ '__/ _ \ '__|
  | | (_| \__ \ < | |  \  /\  / | | | \__ \ |_) \  __/ | |  __/ |
  |_|\__,_|___/_|\_\_|   \/  \/_| |_|_|___/ .__/ \___|_|  \___|_|
                                           |_|
	`)
	fmt.Println("  You whisper. Tasks appear.")
	fmt.Println()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Config error: %v", err)
	}
	log.Println("✅ Configuration loaded")

	ctx := context.Background()

	// Initialize Gemini client
	geminiClient, err := gemini.NewClient(ctx, cfg.GeminiAPIKey)
	if err != nil {
		log.Fatalf("❌ Gemini init error: %v", err)
	}
	defer geminiClient.Close()
	log.Println("✅ Gemini client initialized")

	// Initialize Google Tasks service
	tasksSvc, err := tasks.NewService(ctx, cfg.GoogleCredentialsPath, cfg.GoogleTokenPath)
	if err != nil {
		log.Fatalf("❌ Google Tasks init error: %v", err)
	}
	log.Println("✅ Google Tasks service initialized")

	// Initialize list mapping
	listMapping := tasks.NewListMapping(cfg)

	// Initialize SQLite store
	taskStore, err := store.NewStore(cfg.SQLitePath)
	if err != nil {
		log.Fatalf("❌ SQLite init error: %v", err)
	}
	defer taskStore.Close()
	log.Println("✅ SQLite store initialized")

	// Initialize Telegram bot handler
	handler, err := bot.NewHandler(cfg, geminiClient, tasksSvc, listMapping, taskStore)
	if err != nil {
		log.Fatalf("❌ Bot init error: %v", err)
	}

	// Auto-register webhook if configured
	if cfg.AutoRegisterWebhook {
		if err := handler.RegisterWebhook(); err != nil {
			log.Printf("⚠️ Webhook registration failed: %v", err)
		}
	}

	// Start daily summary scheduler
	sched := scheduler.NewScheduler(tasksSvc, listMapping, handler, cfg)
	sched.Start()
	log.Println("✅ Daily summary scheduler started")

	// Set up HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", handler.HandleWebhook)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	addr := fmt.Sprintf(":%s", cfg.Port)
	server := &http.Server{Addr: addr, Handler: mux}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("\n⏳ Shutting down...")
		server.Close()
	}()

	log.Printf("🚀 TaskWhisperer listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("❌ Server error: %v", err)
	}

	log.Println("👋 TaskWhisperer stopped. Goodbye!")
}
