package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/frontend"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
)

func main() {
	startupDialog := flag.Bool("startup-dialog", true, "show a bounded macOS alert on startup failure; use false for scripts")
	if err := run(); err != nil {
		reportStartupFailure(err, *startupDialog, os.Stderr, showStartupAlert)
		os.Exit(1)
	}
}

func startupMessage(err error) string {
	if errors.Is(err, desktop.ErrWorkspaceInUse) {
		return "detective desktop: 此工作區已由另一個 Detective 程序使用；請回到原視窗，或使用不同的 -data-dir。未改寫設定。"
	}
	return "detective desktop: 無法啟動；請確認私有資料目錄權限與 Desktop 建置。"
}

func run() error {
	dataDir := flag.String("data-dir", "", "absolute private directory for desktop snapshots and checkpoints")
	version := flag.Bool("version", false, "print desktop preview version")
	flag.Parse()
	if *version {
		fmt.Println(desktop.Version)
		return nil
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected desktop arguments")
	}
	if *dataDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*dataDir = filepath.Join(base, "Detective Desktop")
	}
	service, err := desktop.New(*dataDir)
	if err != nil {
		return err
	}
	defer service.Close()
	app := &App{service: service}
	return wails.Run(&options.App{
		Title: windowTitle(service.Snapshot().WorkspaceID), Width: 1360, Height: 880, MinWidth: 1040, MinHeight: 680,
		BackgroundColour: options.NewRGB(247, 247, 242),
		AssetServer:      &assetserver.Options{Assets: frontend.Assets},
		OnStartup:        app.startup,
		OnShutdown:       func(context.Context) { service.Close() },
		Bind:             []any{app},
		Mac:              &mac.Options{TitleBar: mac.TitleBarDefault(), DisableZoom: true},
	})
}

func windowTitle(workspaceID string) string {
	return "AHE Detective — 工作區 " + workspaceID
}
