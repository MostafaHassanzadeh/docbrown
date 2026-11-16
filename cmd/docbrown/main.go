// Command docbrown measures what a PostgreSQL switchover really costs.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/MostafaHassanzadeh/docbrown/internal/config"
	"github.com/MostafaHassanzadeh/docbrown/internal/events"
	"github.com/MostafaHassanzadeh/docbrown/internal/probe"
	"github.com/MostafaHassanzadeh/docbrown/internal/report"
)

var version = "dev"

const usage = `docbrown: every switchover splits time in two.

Usage:
  docbrown pre    [--config docbrown.yaml]
  docbrown probe  [--config docbrown.yaml] [--run ID] [--duration 2m]
  docbrown report [--config docbrown.yaml] [--run ID] [--history FILE]
                  [--controldata FILE] [--waldump FILE]
  docbrown version

Typical session:
  docbrown pre            # snapshot the cluster, create docbrown_probe
  docbrown probe          # keep running; switch over in another terminal; Ctrl+C
  docbrown report         # writes reports/<run>.md and reports/<run>.json

Exit codes: 0 ok, 1 error, 2 report found lost acknowledged writes.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
	var err error
	code := 0
	switch os.Args[1] {
	case "pre":
		err = cmdPre(os.Args[2:])
	case "probe":
		err = cmdProbe(os.Args[2:])
	case "report":
		code, err = cmdReport(os.Args[2:])
	case "version", "--version":
		fmt.Println("docbrown", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "docbrown:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func loadConfig(fs *flag.FlagSet, args []string) (*config.Config, string, error) {
	path := fs.String("config", "docbrown.yaml", "config file")
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(*path)
	return cfg, *path, err
}

func cmdPre(args []string) error {
	fs := flag.NewFlagSet("pre", flag.ExitOnError)
	cfg, path, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	fmt.Println("Cluster before the switchover:")
	run, err := probe.Pre(cfg, path, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Printf("\nRun %s created in %s\nNext: docbrown probe\n", run.ID, run.Dir)
	return nil
}

func cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	runID := fs.String("run", "", "run id (default: latest)")
	duration := fs.Duration("duration", 0, "stop after this long (default: until Ctrl+C)")
	cfg, _, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	run, err := events.OpenRun(cfg.RunsDir, *runID)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	fmt.Printf("Probing run %s every %s. Switch over now; press Ctrl+C when the cluster is settled.\n",
		run.ID, cfg.Interval())
	start := time.Now()
	if err := probe.Run(ctx, cfg, run, os.Stdout); err != nil {
		return err
	}
	fmt.Printf("\nStopped after %s. Next: docbrown report --run %s\n", time.Since(start).Round(time.Millisecond), run.ID)
	return nil
}

func cmdReport(args []string) (int, error) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	runID := fs.String("run", "", "run id (default: latest)")
	var opt report.Options
	fs.StringVar(&opt.HistoryFile, "history", "", "new timeline's .history file, if the server will not show it")
	fs.StringVar(&opt.ControlDataFile, "controldata", "", "pg_controldata output of the old primary")
	fs.StringVar(&opt.WaldumpFile, "waldump", "", "pg_waldump output of the old primary from the fork LSN")
	cfg, _, err := loadConfig(fs, args)
	if err != nil {
		return 1, err
	}
	run, err := events.OpenRun(cfg.RunsDir, *runID)
	if err != nil {
		return 1, err
	}
	s, err := report.Build(cfg, run, opt)
	if err != nil {
		return 1, err
	}
	if err := os.MkdirAll(cfg.ReportsDir, 0o700); err != nil {
		return 1, err
	}
	mdPath := filepath.Join(cfg.ReportsDir, run.ID+".md")
	if err := os.WriteFile(mdPath, []byte(report.Markdown(s)), 0o600); err != nil {
		return 1, err
	}
	jsonPath := filepath.Join(cfg.ReportsDir, run.ID+".json")
	if err := events.WriteJSON(jsonPath, s); err != nil {
		return 1, err
	}
	fmt.Printf("Write downtime: %s   Lost acknowledged writes: %d   In-doubt: %d\n",
		s.WriteDown.Round(time.Millisecond), len(s.Lost), len(s.InDoubtLanded)+len(s.InDoubtGone))
	fmt.Printf("Report: %s\nData:   %s\n", mdPath, jsonPath)
	if len(s.Lost) > 0 {
		return 2, nil
	}
	return 0, nil
}
