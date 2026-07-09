package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mlhiter/sealos-collector/internal/openstatus"
)

func main() {
	var snapshotPath string
	var databaseURL string
	var workspaceSlug string
	var workspaceName string
	var pageSlug string
	var pageTitle string
	var pageDescription string
	var interval time.Duration
	var snapshotMaxAge time.Duration
	var includeInternal bool
	var showUptime bool

	flag.StringVar(&snapshotPath, "snapshot", "summary.json", "path to collector summary.json")
	flag.StringVar(&databaseURL, "database-url", getenv("OPENSTATUS_DATABASE_URL", ""), "OpenStatus libSQL HTTP URL")
	flag.StringVar(&workspaceSlug, "workspace-slug", "sealos", "OpenStatus workspace slug")
	flag.StringVar(&workspaceName, "workspace-name", "Sealos", "OpenStatus workspace name")
	flag.StringVar(&pageSlug, "page-slug", "sealos-status", "OpenStatus status page slug")
	flag.StringVar(&pageTitle, "page-title", "", "OpenStatus status page title; defaults to snapshot cluster name")
	flag.StringVar(&pageDescription, "page-description", "Sealos platform health collected from read-only cluster evidence.", "OpenStatus status page description")
	flag.DurationVar(&interval, "interval", 0, "sync repeatedly at this interval; 0 runs once")
	flag.DurationVar(&snapshotMaxAge, "snapshot-max-age", 5*time.Minute, "fallback max snapshot age when summary.json does not include freshness metadata")
	flag.BoolVar(&includeInternal, "include-internal", true, "include non-user-facing platform components")
	flag.BoolVar(&showUptime, "show-uptime", false, "show OpenStatus uptime/monitors page")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	options := openstatus.Options{
		DatabaseURL:     databaseURL,
		WorkspaceSlug:   workspaceSlug,
		WorkspaceName:   workspaceName,
		PageSlug:        pageSlug,
		PageTitle:       pageTitle,
		PageDescription: pageDescription,
		IncludeInternal: includeInternal,
		ShowUptime:      showUptime,
		SnapshotMaxAge:  snapshotMaxAge,
	}
	syncer, err := openstatus.NewSyncer(options)
	if err != nil {
		exitf("init syncer: %v", err)
	}

	if interval <= 0 {
		if err := syncOnce(ctx, syncer, snapshotPath); err != nil {
			exitf("sync: %v", err)
		}
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := syncOnce(ctx, syncer, snapshotPath); err != nil {
			fmt.Fprintf(os.Stderr, "sync: %v\n", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func syncOnce(ctx context.Context, syncer *openstatus.Syncer, snapshotPath string) error {
	snapshot, err := openstatus.LoadSnapshot(snapshotPath)
	if err != nil {
		return err
	}
	result, err := syncer.Sync(ctx, snapshot)
	if err != nil {
		return err
	}
	fmt.Fprintf(
		os.Stdout,
		"synced page=%d workspace=%d components=%d created=%d updated=%d resolved=%d removed=%d\n",
		result.PageID,
		result.WorkspaceID,
		result.Components,
		result.ReportsCreated,
		result.ReportsUpdated,
		result.ReportsResolved,
		result.StaleRemoved,
	)
	return nil
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
