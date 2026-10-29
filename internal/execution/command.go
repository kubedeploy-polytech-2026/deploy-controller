package execution

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"kubedeploy/deploy-controller/internal/intake"
)

type Command func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)

func RunCommand(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if key != "GH_TOKEN" && key != "GITHUB_TOKEN" && key != "GH_ENTERPRISE_TOKEN" && key != "GITHUB_ENTERPRISE_TOKEN" {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s failed: %w", name, err)
	}
	return out, nil
}

type Commands struct {
	Run      Command
	WorkDir  string
	dir      string
	revision string
	env      []string
}

func (c *Commands) Ready(ctx context.Context) error {
	_, err := c.Run(ctx, "", nil, "kubectl", "--context", "k3d-kubedeploy", "--request-timeout=5s", "get", "--raw=/readyz")
	return err
}

func (c *Commands) Close() error {
	if c.dir == "" {
		return nil
	}
	err := os.RemoveAll(c.dir)
	c.dir = ""
	c.revision = ""
	return err
}

func (c *Commands) Prepare(ctx context.Context, p intake.Plan) error {
	if err := validatePlan(p); err != nil {
		return err
	}
	if c.dir != "" && c.revision == p.EnvironmentRevision {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp(c.WorkDir, "environment-*")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(dir)
		}
	}()
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"fetch", "--quiet", "--depth=1", "https://github.com/" + intake.Repository + ".git", p.EnvironmentRevision},
		{"-c", "core.hooksPath=/dev/null", "checkout", "--quiet", "--detach", "FETCH_HEAD"},
	} {
		if _, err = c.Run(ctx, dir, env, "git", args...); err != nil {
			return err
		}
	}
	actual, err := c.Run(ctx, dir, env, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(actual)) != p.EnvironmentRevision {
		return fmt.Errorf("checkout revision mismatch")
	}
	contract, err := c.Run(ctx, dir, env, "make", "-s", "execution-contract")
	if err != nil || strings.TrimSpace(string(contract)) != "1" {
		return fmt.Errorf("environment revision does not support separate apply/wait")
	}
	if err = c.Close(); err != nil {
		return err
	}
	c.dir, c.revision, c.env = dir, p.EnvironmentRevision, env
	keep = true
	return nil
}

func (c *Commands) make(ctx context.Context, p intake.Plan, args ...string) error {
	if err := c.Prepare(ctx, p); err != nil {
		return err
	}
	out, err := c.Run(ctx, c.dir, c.env, "make", args...)
	if len(out) > 0 {
		fmt.Fprint(os.Stderr, string(out))
	}
	return err
}

func (c *Commands) Apply(ctx context.Context, p intake.Plan) (Snapshot, error) {
	if err := c.make(ctx, p, "apply", "BACKEND_IMAGE="+p.BackendImage, "FRONTEND_IMAGE="+p.FrontendImage); err != nil {
		return nil, err
	}
	snapshot, _, _, err := c.snapshot(ctx, p)
	return snapshot, err
}

func (c *Commands) Wait(ctx context.Context, p intake.Plan, snapshot Snapshot, deadline time.Time) error {
	ready, err := c.check(ctx, p, snapshot)
	if err != nil || ready {
		return err
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	seconds := int64((remaining + time.Second - 1) / time.Second)
	if seconds > 3600 {
		seconds = 3600
	}
	err = c.make(ctx, p, "wait", fmt.Sprintf("ROLLOUT_TIMEOUT_SECONDS=%d", seconds))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ready, checkErr := c.check(ctx, p, snapshot)
	if checkErr != nil {
		return checkErr
	}
	if ready {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("rollout has not reached the expected generation")
}

func (c *Commands) Smoke(ctx context.Context, p intake.Plan, snapshot Snapshot) error {
	ready, err := c.check(ctx, p, snapshot)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("applications are not ready for smoke")
	}
	if err = c.make(ctx, p, "smoke"); err != nil {
		return err
	}
	ready, err = c.check(ctx, p, snapshot)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("applications became unready during smoke")
	}
	return nil
}
