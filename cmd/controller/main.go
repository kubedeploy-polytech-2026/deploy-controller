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

	"kubedeploy/deploy-controller/internal/execution"
	"kubedeploy/deploy-controller/internal/githubapp"
	"kubedeploy/deploy-controller/internal/intake"
)

func run(ctx context.Context, once bool, interval time.Duration, step func(context.Context) (intake.Plan, error)) error {
	for {
		pollCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		plan, err := step(pollCtx)
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
			if plan.State == "failure" || plan.State == "error" {
				return fmt.Errorf("deployment finished with %s", plan.State)
			}
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
	execute := flag.Bool("execute", false, "Apply verified requests and publish deployment status")
	stateDir := flag.String("state-dir", "", "Persistent local state directory for one executor")
	flag.Parse()
	if *interval < 10*time.Second || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "expected no arguments and interval >= 10s")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	github := intake.GitHub{Run: intake.RunGH}
	config, appEnabled, err := githubapp.FromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if appEnabled {
		source, err := githubapp.New(config)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		github.Run = githubapp.Runner(source, intake.RunGHWithEnv)
	}
	step := func(ctx context.Context) (intake.Plan, error) {
		verifyCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		return intake.Poll(verifyCtx, github, time.Now)
	}
	if *execute {
		store, err := execution.Open(*stateDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer store.Close()
		controller := execution.Controller{GitHub: github, Executor: execution.Commands{Run: execution.RunCommand, WorkDir: *stateDir}, Store: store, Now: time.Now}
		step = controller.Step
	}
	if err := run(ctx, *once, *interval, step); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
