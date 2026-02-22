package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"taskwhisperer/bot"
	"taskwhisperer/config"
	"taskwhisperer/llm"
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

	// Initialize LLM client
	llmClient := llm.NewClient(cfg.OpenRouterAPIKey)
	log.Println("✅ LLM client initialized")

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
	handler, err := bot.NewHandler(cfg, llmClient, tasksSvc, listMapping, taskStore)
	if err != nil {
		log.Fatalf("❌ Bot init error: %v", err)
	}

	// Start daily summary and reminders scheduler
	sched := scheduler.NewScheduler(tasksSvc, listMapping, handler, taskStore, cfg)
	sched.Start()
	log.Println("✅ Daily summary and reminders scheduler started")

	// Wire up the summary generator to the bot handler for the /today command
	handler.SetSummaryGenerator(func() string {
		return sched.GenerateSummary()
	})

	// Graceful shutdown via signal
	stopCh := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("\n⏳ Shutting down...")
		close(stopCh)
	}()

	// Start long-polling (blocks until stopCh is closed)
	log.Println("🚀 TaskWhisperer started (polling mode)")
	handler.StartPolling(stopCh)

	log.Println("👋 TaskWhisperer stopped. Goodbye!")
}
