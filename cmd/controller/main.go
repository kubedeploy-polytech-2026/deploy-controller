package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kubedeploy/deploy-controller/internal/execution"
	"kubedeploy/deploy-controller/internal/githubapp"
	"kubedeploy/deploy-controller/internal/health"
	"kubedeploy/deploy-controller/internal/intake"
)

func run(ctx context.Context, once bool, interval time.Duration, step func(context.Context) (intake.Plan, error)) error {
	failures := 0
	for {
		plan, err := step(ctx)
		if ctx.Err() != nil {
			return nil
		}
		delay := interval
		if err != nil {
			if once {
				return err
			}
			fmt.Fprintln(os.Stderr, "poll:", err)
			if failures < 4 {
				failures++
			}
			delay = 2 * time.Second * time.Duration(1<<failures)
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			delay = delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
		} else {
			failures = 0
			if err := json.NewEncoder(os.Stdout).Encode(plan); err != nil {
				return err
			}
		}
		if once {
			if plan.State == "failure" || plan.State == "error" {
				return fmt.Errorf("deployment finished with %s", plan.State)
			}
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func mainRun() error {
	once := flag.Bool("once", false, "Read and verify the newest request, then exit")
	interval := flag.Duration("interval", 30*time.Second, "Delay between polls (minimum 10s)")
	execute := flag.Bool("execute", false, "Apply verified requests and publish deployment status")
	budgets := execution.DefaultBudgets()
	flag.DurationVar(&budgets.Total, "deployment-timeout", budgets.Total, "Total deployment budget")
	flag.DurationVar(&budgets.Apply, "apply-timeout", budgets.Apply, "Maximum time for applying resources")
	flag.DurationVar(&budgets.Rollout, "rollout-timeout", budgets.Rollout, "Shared rollout budget")
	flag.DurationVar(&budgets.Smoke, "smoke-timeout", budgets.Smoke, "Smoke check budget")
	stateDir := flag.String("state-dir", "", "Persistent local state directory for one executor")
	healthAddress := flag.String("health-address", "", "HTTP probe listen address (disabled when empty)")
	flag.Parse()
	if *interval < 10*time.Second || flag.NArg() != 0 {
		return fmt.Errorf("expected no arguments and interval >= 10s")
	}
	if err := budgets.Validate(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	github := intake.GitHub{Run: intake.RunGH}
	config, appEnabled, err := githubapp.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if appEnabled {
		source, err := githubapp.New(config)
		if err != nil {
			return err
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
			return err
		}
		defer store.Close()
		commands := &execution.Commands{Run: execution.RunCommand, WorkDir: *stateDir}
		defer commands.Close()
		controller := execution.Controller{GitHub: github, Executor: commands, Store: store, Now: time.Now, Budgets: budgets}
		step = controller.Step
	}
	status := &health.Status{}
	var serverErrors <-chan error
	if *healthAddress != "" {
		listener, err := net.Listen("tcp", *healthAddress)
		if err != nil {
			return fmt.Errorf("health listener: %w", err)
		}
		server := &http.Server{Handler: status.Handler(), ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
		defer server.Close()
		failures := make(chan error, 1)
		serverErrors = failures
		go func() {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				failures <- err
				stop()
			}
		}()
	}
	err = run(ctx, *once, *interval, func(ctx context.Context) (intake.Plan, error) {
		plan, err := step(ctx)
		status.Record(err)
		return plan, err
	})
	select {
	case serverErr := <-serverErrors:
		return fmt.Errorf("health server: %w", serverErr)
	default:
		return err
	}
}

func main() {
	if err := mainRun(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
