package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/detectivehost"
)

const version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type commandArgs struct {
	configPath      string
	applyMigrations bool
	help            bool
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	getenv func(string) string,
) error {
	parsed, err := parseCommandArgs(args)
	if err != nil {
		return err
	}
	if parsed.help {
		fmt.Fprintln(stdout, usage())
		return nil
	}
	config, err := detectivehost.LoadConfig(parsed.configPath)
	if err != nil {
		return err
	}
	databaseURL := strings.TrimSpace(getenv("DATABASE_DNS"))
	if databaseURL == "" {
		return errors.New("DATABASE_DNS is required")
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil)).With(
		"service", "ahe-detective",
		"version", version,
	)
	return detectivehost.Run(ctx, databaseURL, config, detectivehost.Options{
		ApplyMigrations: parsed.applyMigrations,
		Logger:          logger,
	})
}

func parseCommandArgs(args []string) (commandArgs, error) {
	var parsed commandArgs
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-h" || arg == "--help":
			parsed.help = true
		case arg == "--migrate":
			parsed.applyMigrations = true
		case strings.HasPrefix(arg, "--config="):
			parsed.configPath = strings.TrimSpace(strings.TrimPrefix(arg, "--config="))
		case arg == "--config":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return commandArgs{}, errors.New("--config requires a path")
			}
			parsed.configPath = strings.TrimSpace(args[index+1])
			index++
		default:
			return commandArgs{}, fmt.Errorf("unknown argument %q", arg)
		}
	}
	if parsed.help {
		return parsed, nil
	}
	if parsed.configPath == "" {
		return commandArgs{}, errors.New("--config is required")
	}
	return parsed, nil
}

func usage() string {
	return strings.Join([]string{
		"Usage: ahe-detective --config PATH [--migrate]",
		"",
		"Options:",
		"  --config PATH  Strict ahe-detective-host-v1/v2/v3/v4 JSON configuration",
		"  --migrate      Explicit first-start compatibility path; prefer ahe-migrate",
		"  -h, --help     Show this help",
		"",
		"Environment:",
		"  DATABASE_DNS   PostgreSQL DSN for the authoritative AHE store",
	}, "\n")
}
