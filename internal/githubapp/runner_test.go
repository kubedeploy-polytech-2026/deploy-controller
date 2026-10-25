package githubapp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeTokens struct {
	repo  string
	calls int
	err   error
}

func (f *fakeTokens) Token(_ context.Context, repo string) (string, error) {
	f.repo = repo
	f.calls++
	return "installation-token", f.err
}

func TestRunnerScopesAndPrivateEnvironment(t *testing.T) {
	cases := []struct {
		args []string
		repo string
	}{
		{[]string{"api", "--hostname", "github.com", "repos/kubedeploy-polytech-2026/environment/deployments?per_page=100", "--paginate", "--slurp"}, "environment"},
		{[]string{"api", "--hostname", "github.com", "--method", "POST", "repos/kubedeploy-polytech-2026/environment/deployments/42/statuses", "-f", "state=success"}, "environment"},
		{[]string{"api", "--hostname", "github.com", "repos/kubedeploy-polytech-2026/backend/releases/assets/42", "-H", "Accept: application/octet-stream"}, "backend"},
		{[]string{"attestation", "verify", "oci://image", "--hostname", "github.com", "--repo", "kubedeploy-polytech-2026/frontend"}, "frontend"},
	}
	for _, tc := range cases {
		t.Run(tc.repo+tc.args[0], func(t *testing.T) {
			tokens := &fakeTokens{}
			calls := 0
			run := Runner(tokens, func(_ context.Context, env []string, args ...string) ([]byte, error) {
				calls++
				if !reflect.DeepEqual(args, tc.args) {
					t.Fatal("command changed")
				}
				if !reflect.DeepEqual(env, []string{"GH_TOKEN=installation-token", "GITHUB_TOKEN=", "GH_DEBUG=", "GITHUB_APP_PRIVATE_KEY_FILE="}) {
					t.Fatal("unexpected credential environment")
				}
				return []byte(`{}`), nil
			})
			if _, err := run(context.Background(), tc.args...); err != nil {
				t.Fatal(err)
			}
			if tokens.repo != tc.repo || tokens.calls != 1 || calls != 1 {
				t.Fatal("incorrect token scope")
			}
		})
	}
}

func TestRunnerRejectsUnknownTargetsAndTokenFailure(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"auth", "token", "--hostname", "github.com"},
		{"api", "repos/kubedeploy-polytech-2026/backend/releases"},
		{"api", "--hostname", "attacker.example", "repos/kubedeploy-polytech-2026/backend/releases"},
		{"api", "--hostname", "github.com", "repos/kubedeploy-polytech-2026/backend/releases", "--hostname", "attacker.example"},
		{"api", "--hostname", "github.com", "https://attacker.example", "-f", "repos/kubedeploy-polytech-2026/backend/releases"},
		{"api", "--hostname", "github.com", "repos/attacker/backend/releases"},
		{"attestation", "verify", "oci://image", "--hostname", "github.com", "--repo", "attacker/backend"},
	} {
		tokens := &fakeTokens{}
		run := Runner(tokens, func(context.Context, []string, ...string) ([]byte, error) {
			t.Fatal("unsupported command executed")
			return nil, nil
		})
		if _, err := run(context.Background(), args...); err == nil || tokens.calls != 0 {
			t.Fatal("unsupported command requested credentials")
		}
	}
	tokens := &fakeTokens{err: errors.New("cannot mint token")}
	run := Runner(tokens, func(context.Context, []string, ...string) ([]byte, error) {
		t.Fatal("fell back after app auth failure")
		return nil, nil
	})
	if _, err := run(context.Background(), "api", "--hostname", "github.com", "repos/kubedeploy-polytech-2026/environment/deployments"); err == nil || !strings.Contains(err.Error(), "cannot mint") {
		t.Fatal("lost app auth failure")
	}
}
