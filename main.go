// Package main implements a Discord Threat Intelligence Bot that monitors
// ransomware.live API and RSS feeds, delivering real-time cybersecurity alerts
// to Discord channels via webhooks.
//
// The bot uses a modular architecture with separate components for:
// - API polling and RSS feed parsing
// - Message formatting and Discord webhook delivery
// - Persistent status tracking and deduplication
// - Configurable logging with rotation
//
// Configuration is managed through JSON files in the config directory.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"Ransomware-Bot/internal/config"
	"Ransomware-Bot/internal/logger"
	"Ransomware-Bot/internal/scheduler"

	log "github.com/sirupsen/logrus"
)

// Application version
const Version = "1.1.0"

func main() {
	// Parse command line flags
	configDir := flag.String("config-dir", "./configs", "Directory containing configuration files")
	dataDir := flag.String("data-dir", "", "Directory for persistent data (overrides config and DATA_DIR env)")
	version := flag.Bool("version", false, "Show version information")
	checkConfig := flag.Bool("check-config", false, "Validate configuration and exit")
	dryRun := flag.Bool("dry-run", false, "Preview mode: format messages and log them without sending")
	flag.Parse()

	// Show version and exit if requested
	if *version {
		fmt.Printf("Discord Threat Intelligence Bot v%s\n", Version)
		os.Exit(0)
	}

	// Validate config directory exists
	if _, err := os.Stat(*configDir); os.IsNotExist(err) {
		fmt.Printf("Error: Config directory '%s' does not exist\n", *configDir)
		os.Exit(1)
	}

	// Check-config mode: validate and exit
	if *checkConfig {
		_, err := config.LoadConfig(*configDir)
		if err != nil {
			fmt.Printf("Configuration INVALID: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Configuration OK")
		os.Exit(0)
	}

	// Load configuration
	cfg, err := config.LoadConfig(*configDir)
	if err != nil {
		fmt.Printf("Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	// Override DataDir: flag > env > config > default
	resolved, err := resolveDataDir(*dataDir, cfg.DataDir)
	if err != nil {
		fmt.Printf("Error resolving data-dir path: %v\n", err)
		os.Exit(1)
	}
	cfg.DataDir = resolved

	// Setup logging
	logDir := "./logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Printf("Error creating logs directory: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger with configurable rotation settings
	rotationConfig := logger.LogRotationConfig{
		MaxSizeMB:  cfg.LogRotation.MaxSizeMB,
		MaxBackups: cfg.LogRotation.MaxBackups,
		MaxAgeDays: cfg.LogRotation.MaxAgeDays,
		Compress:   cfg.LogRotation.Compress,
	}
	err = logger.NewLogger(cfg.LogLevel, filepath.Join(logDir, "bot.log"), rotationConfig)
	if err != nil {
		fmt.Printf("Error setting up logger: %v\n", err)
		os.Exit(1)
	}

	// Log initial message
	log.WithField("version", Version).Info("Starting Threat Intelligence Bot")

	if *dryRun {
		log.Info("Dry-run mode enabled: messages will be logged but not sent")
	}

	// Create application context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize scheduler
	sched, err := scheduler.New(cfg, *configDir, *dryRun)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize scheduler")
	}

	// Dry-run mode: run single cycle and exit
	if *dryRun {
		sched.RunOnce(ctx)
		sched.Stop()
		log.Info("Dry-run completed")
		if err := logger.Close(); err != nil {
			fmt.Printf("Warning: Failed to close logger: %v\n", err)
		}
		os.Exit(0)
	}

	// Start scheduler
	if err := sched.Start(ctx); err != nil {
		log.WithError(err).Fatal("Failed to start scheduler")
	}

	log.Info("Bot started successfully")

	// Setup signal handling for graceful shutdown
	// Listens for SIGINT (Ctrl+C) and SIGTERM (systemd/docker stop)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Wait for shutdown signal
	// This blocks until a termination signal is received
	<-sigChan
	log.Info("Shutdown signal received, stopping bot...")

	// Cancel context to signal all goroutines to stop
	cancel()

	// Stop scheduler
	sched.Stop()

	log.Info("Bot stopped successfully")

	// Close logger to flush and release log file (must be last)
	if err := logger.Close(); err != nil {
		fmt.Printf("Warning: Failed to close logger: %v\n", err)
	}
}

// resolveDataDir determines the final DataDir value using the priority:
// flag > DATA_DIR env > config value.
// All non-empty paths are resolved to absolute paths.
func resolveDataDir(flagVal, configVal string) (string, error) {
	if flagVal != "" {
		return filepath.Abs(flagVal)
	}
	if envDir := os.Getenv("DATA_DIR"); envDir != "" {
		return filepath.Abs(envDir)
	}
	if configVal != "" {
		return filepath.Abs(configVal)
	}
	return configVal, nil
}

