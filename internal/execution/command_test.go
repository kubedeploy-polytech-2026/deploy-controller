package execution

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"kubedeploy/deploy-controller/internal/intake"
)

func TestPinnedCheckoutAndDeploy(t *testing.T) {
	sha := strings.Repeat("a", 40)
	p := intake.Plan{EnvironmentRevision: sha, BackendImage: "ghcr.io/" + intake.Organization + "/backend@sha256:" + strings.Repeat("b", 64), FrontendImage: "ghcr.io/" + intake.Organization + "/frontend@sha256:" + strings.Repeat("c", 64)}
	var commands [][]string
	var checkout string
	c := Commands{WorkDir: t.TempDir(), Run: func(_ context.Context, dir string, _ []string, name string, args ...string) ([]byte, error) {
		checkout = dir
		commands = append(commands, append([]string{name}, args...))
		if name == "git" && args[0] == "rev-parse" {
			return []byte(sha + "\n"), nil
		}
		return nil, nil
	}}
	if err := c.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commands[1], []string{"git", "fetch", "--quiet", "--depth=1", "https://github.com/" + intake.Repository + ".git", sha}) {
		t.Fatalf("checkout not pinned: %v", commands)
	}
	if !reflect.DeepEqual(commands[len(commands)-1], []string{"make", "deploy", "BACKEND_IMAGE=" + p.BackendImage, "FRONTEND_IMAGE=" + p.FrontendImage}) {
		t.Fatalf("wrong deploy: %v", commands)
	}
	if _, err := os.Stat(checkout); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("checkout was not cleaned up")
	}
	commands = nil
	p.BackendImage = "ghcr.io/attacker/backend:latest"
	if err := c.Apply(context.Background(), p); err == nil || len(commands) != 0 {
		t.Fatal("accepted arbitrary image")
	}
}

func TestWrongCheckoutNeverDeploys(t *testing.T) {
	p := intake.Plan{EnvironmentRevision: strings.Repeat("a", 40), BackendImage: "ghcr.io/" + intake.Organization + "/backend@sha256:" + strings.Repeat("b", 64), FrontendImage: "ghcr.io/" + intake.Organization + "/frontend@sha256:" + strings.Repeat("c", 64)}
	c := Commands{WorkDir: t.TempDir(), Run: func(_ context.Context, _ string, _ []string, name string, _ ...string) ([]byte, error) {
		if name == "make" {
			t.Fatal("deployed wrong checkout")
		}
		return []byte("wrong"), nil
	}}
	if err := c.Apply(context.Background(), p); err == nil {
		t.Fatal("accepted wrong revision")
	}
}

func TestCommandCancellationAndCredentials(t *testing.T) {
	t.Setenv("GH_TOKEN", "must-not-inherit")
	out, err := RunCommand(context.Background(), "", nil, "sh", "-c", `test -z "${GH_TOKEN:-}" && printf ok`)
	if err != nil || string(out) != "ok" {
		t.Fatalf("credential inherited: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := RunCommand(ctx, "", nil, "sh", "-c", "sleep 20 & wait"); err == nil {
		t.Fatal("cancelled command succeeded")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("child process outlived cancellation")
	}
}
