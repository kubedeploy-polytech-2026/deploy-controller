package execution

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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
	Run     Command
	WorkDir string
}

func (c Commands) Ready(ctx context.Context) error {
	_, err := c.Run(ctx, "", nil, "kubectl", "--context", "k3d-kubedeploy", "--request-timeout=5s", "get", "--raw=/readyz")
	return err
}

func (c Commands) Apply(ctx context.Context, p intake.Plan) error {
	sha := regexp.MustCompile(`^[a-f0-9]{40}$`)
	for component, image := range map[string]string{"backend": p.BackendImage, "frontend": p.FrontendImage} {
		pattern := `^ghcr\.io/` + regexp.QuoteMeta(intake.Organization+"/"+component) + `@sha256:[a-f0-9]{64}$`
		if !regexp.MustCompile(pattern).MatchString(image) {
			return fmt.Errorf("invalid %s image", component)
		}
	}
	if !sha.MatchString(p.EnvironmentRevision) {
		return fmt.Errorf("invalid environment revision")
	}
	dir, err := os.MkdirTemp(c.WorkDir, "environment-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
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
	out, err := c.Run(ctx, dir, env, "make", "deploy", "BACKEND_IMAGE="+p.BackendImage, "FRONTEND_IMAGE="+p.FrontendImage)
	if len(out) > 0 {
		fmt.Fprint(os.Stderr, string(out))
	}
	return err
}
