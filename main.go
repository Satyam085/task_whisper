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
	llmClient, err := llm.NewClient(ctx, cfg.GeminiAPIKey)
	if err != nil {
		log.Fatalf("❌ Gemini client init error: %v", err)
	}
	defer llmClient.Close()
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

	var senders []scheduler.MessageSender

	// Initialize Telegram bot handler conditionally
	var tgBot *bot.TelegramBot
	if cfg.TelegramToken != "" {
		tgBot, err = bot.NewTelegramBot(cfg, llmClient, tasksSvc, listMapping, taskStore)
		if err != nil {
			log.Fatalf("❌ Telegram Bot init error: %v", err)
		}
		senders = append(senders, tgBot)
	}


	if len(senders) == 0 {
		log.Fatalf("❌ No bots configured! Please set either TELEGRAM_TOKEN or Google Chat settings in config.")
	}

	// Start daily summary and reminders scheduler
	sched := scheduler.NewScheduler(tasksSvc, listMapping, senders, taskStore, cfg)
	sched.Start()
	log.Println("✅ Daily summary and reminders scheduler started")

	// Wire up the summary generator to the bot handlers for the /today command
	if tgBot != nil {
		tgBot.SetSummaryGenerator(func() string {
			return sched.GenerateSummary()
		})
		tgBot.SetWeeklySummaryGenerator(func() string {
			return sched.GenerateWeeklySummary()
		})
	}

	// Graceful shutdown via signal
	stopCh := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("\n⏳ Shutting down...")
		close(stopCh)
	}()

	log.Println("🚀 TaskWhisperer started")

	if tgBot != nil {
		tgBot.StartPolling(stopCh) // Blocking
	}

	log.Println("👋 TaskWhisperer stopped. Goodbye!")
}
