package githubapp

import (
	"context"
	"fmt"
	"strings"
)

type Tokens interface {
	Token(context.Context, string) (string, error)
}
type Command func(context.Context, []string, ...string) ([]byte, error)

func Runner(tokens Tokens, command Command) func(context.Context, ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		repository, err := repositoryFor(args)
		if err != nil {
			return nil, err
		}
		token, err := tokens.Token(ctx, repository)
		if err != nil {
			return nil, err
		}
		return command(ctx, []string{"GH_TOKEN=" + token, "GITHUB_TOKEN=", "GH_DEBUG=", "GITHUB_APP_PRIVATE_KEY_FILE="}, args...)
	}
}

func repositoryFor(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("missing GitHub CLI command")
	}
	hosts := 0
	for i, arg := range args {
		if strings.HasPrefix(arg, "--hostname=") {
			return "", fmt.Errorf("unsupported GitHub hostname flag")
		}
		if arg == "--hostname" {
			if i+1 >= len(args) || args[i+1] != "github.com" {
				return "", fmt.Errorf("GitHub App authentication requires github.com")
			}
			hosts++
		}
	}
	if hosts != 1 {
		return "", fmt.Errorf("expected one github.com hostname")
	}
	if args[0] == "api" {
		endpoint := 3
		if len(args) > 4 && args[3] == "--method" && args[4] == "POST" {
			endpoint = 5
		}
		if len(args) > endpoint && args[1] == "--hostname" && strings.HasPrefix(args[endpoint], "repos/kubedeploy-polytech-2026/") {
			component, rest, ok := strings.Cut(strings.TrimPrefix(args[endpoint], "repos/kubedeploy-polytech-2026/"), "/")
			if ok && rest != "" && (component == "environment" || component == "backend" || component == "frontend") {
				return component, nil
			}
		}
	}
	if len(args) > 2 && args[0] == "attestation" && args[1] == "verify" {
		for i, arg := range args {
			if arg == "--repo" && i+1 < len(args) {
				switch args[i+1] {
				case "kubedeploy-polytech-2026/backend":
					return "backend", nil
				case "kubedeploy-polytech-2026/frontend":
					return "frontend", nil
				}
			}
		}
	}
	return "", fmt.Errorf("unsupported command for GitHub App authentication")
}
