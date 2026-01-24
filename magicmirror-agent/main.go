package main

import (
	"flag"
	"log"
	"os"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/api"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/config"
)

func main() {
	file, err := os.Create("/var/log/magic-mirror/programMagicMirrorAgent.log")
	if err != nil {
		log.Fatalf("failed to create log file: %v", err)
	}
	defer file.Close()

	log.SetOutput(file)
	log.Println("Message to file")
	
	configPath := flag.String("config", "/etc/magicmirror-agent/config.yaml", "Path to agent configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	log.Fatalf("Skyler Config Path: %s", *configPath)
	if err != nil {
		log.Printf("Warning: Could not load config file, using defaults: %v", err)
		cfg = config.DefaultConfig()
	}

	// Validate Magic Mirror config path exists
	if _, err := os.Stat(cfg.MagicMirror.ConfigPath); os.IsNotExist(err) {
		log.Fatalf("Magic Mirror config not found at: %s", cfg.MagicMirror.ConfigPath)
	}

	server := api.NewServer(cfg)
	log.Printf("Starting Magic Mirror Agent on %s:%d", cfg.Server.Host, cfg.Server.Port)
	if err := server.Run(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
