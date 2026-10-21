package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kubedeploy/deploy-controller/internal/intake"
)

func run(ctx context.Context, once bool, interval time.Duration) error {
	for {
		pollCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		plan, err := intake.Poll(pollCtx, intake.GitHub{Run: intake.RunGH}, time.Now)
		cancel()
		if err != nil {
			if once {
				return err
			}
			fmt.Fprintln(os.Stderr, "poll:", err)
		} else if err := json.NewEncoder(os.Stdout).Encode(plan); err != nil {
			return err
		}
		if once {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func main() {
	once := flag.Bool("once", false, "Read and verify the newest request, then exit")
	interval := flag.Duration("interval", 30*time.Second, "Delay between polls (minimum 10s)")
	flag.Parse()
	if *interval < 10*time.Second || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "expected no arguments and interval >= 10s")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *once, *interval); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
