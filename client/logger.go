package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
)

// setupLogger configures the global logger based on CLI flags
// Returns the dev mode flag value for use elsewhere
func setupLogger() bool {
	var devMode bool
	var jsonOutput bool

	var showVersion bool

	flag.BoolVar(&devMode, "dev", false, "Enable dev mode (debug logging enabled)")
	flag.BoolVar(&jsonOutput, "json", false, "Output logs in JSON format")
	flag.BoolVar(&showVersion, "version", false, "Print the client version and exit")
	flag.BoolVar(&showVersion, "v", false, "Print the client version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println("muc-client " + Version)
		os.Exit(0)
	}

	// Set log level
	level := slog.LevelInfo
	if devMode {
		level = slog.LevelDebug
	}

	// Create handler based on output format
	var handler slog.Handler
	if jsonOutput {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
	} else {
		// Text handler with syslog-like format
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
	}

	// Set as default logger
	slog.SetDefault(slog.New(handler))

	return devMode
}
