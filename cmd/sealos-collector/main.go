package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mlhiter/sealos-collector/internal/collector"
	"github.com/mlhiter/sealos-collector/internal/config"
	"github.com/mlhiter/sealos-collector/internal/status"
)

func main() {
	var configPath string
	var outputPath string
	var statePath string
	var interval time.Duration
	var pretty bool

	flag.StringVar(&configPath, "config", "configs/sealos.example.yaml", "path to collector config")
	flag.StringVar(&outputPath, "output", "summary.json", "output path, or - for stdout")
	flag.StringVar(&statePath, "state", "", "optional persisted collector state path")
	flag.DurationVar(&interval, "interval", 0, "collect repeatedly at this interval; 0 runs once")
	flag.BoolVar(&pretty, "pretty", true, "pretty-print JSON output")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(configPath)
	if err != nil {
		exitf("load config: %v", err)
	}
	runner, err := collector.NewWithState(cfg, statePath)
	if err != nil {
		exitf("init collector: %v", err)
	}

	if interval <= 0 {
		snapshot, err := runner.Collect(ctx)
		if err != nil {
			exitf("collect: %v", err)
		}
		if err := writeSnapshot(outputPath, snapshot, pretty); err != nil {
			exitf("write snapshot: %v", err)
		}
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		snapshot, err := runner.Collect(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "collect: %v\n", err)
		} else if err := writeSnapshot(outputPath, snapshot, pretty); err != nil {
			fmt.Fprintf(os.Stderr, "write snapshot: %v\n", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func writeSnapshot(path string, snapshot *status.Snapshot, pretty bool) error {
	var raw []byte
	var err error
	if pretty {
		raw, err = json.MarshalIndent(snapshot, "", "  ")
	} else {
		raw, err = json.Marshal(snapshot)
	}
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	if path == "-" {
		_, err = os.Stdout.Write(raw)
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
