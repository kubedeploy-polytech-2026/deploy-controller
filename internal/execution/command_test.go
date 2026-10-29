package execution

import (
	"context"
	"encoding/json"
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
		if dir != "" {
			checkout = dir
		}
		commands = append(commands, append([]string{name}, args...))
		if name == "git" && args[0] == "rev-parse" {
			return []byte(sha + "\n"), nil
		}
		if name == "make" && len(args) > 1 && args[1] == "execution-contract" {
			return []byte("1"), nil
		}
		if name == "kubectl" {
			return deploymentResponse(p, true, nil), nil
		}
		return nil, nil
	}}
	if _, err := c.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commands[1], []string{"git", "fetch", "--quiet", "--depth=1", "https://github.com/" + intake.Repository + ".git", sha}) {
		t.Fatalf("checkout not pinned: %v", commands)
	}
	if !reflect.DeepEqual(commands[len(commands)-2], []string{"make", "apply", "BACKEND_IMAGE=" + p.BackendImage, "FRONTEND_IMAGE=" + p.FrontendImage}) {
		t.Fatalf("wrong deploy: %v", commands)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(checkout); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("checkout was not cleaned up")
	}
	commands = nil
	p.BackendImage = "ghcr.io/attacker/backend:latest"
	if _, err := c.Apply(context.Background(), p); err == nil || len(commands) != 0 {
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
	if _, err := c.Apply(context.Background(), p); err == nil {
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

func deploymentResponse(p intake.Plan, ready bool, mutate func(map[string]any)) []byte {
	var items []any
	for name, image := range map[string]string{"backend": p.BackendImage, "frontend": p.FrontendImage} {
		count := 0
		if ready {
			count = 1
		}
		d := map[string]any{
			"metadata": map[string]any{"name": name, "uid": name + "-uid", "generation": 1},
			"spec":     map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"image": image}}}}},
			"status":   map[string]any{"observedGeneration": 1, "replicas": 1, "readyReplicas": count, "updatedReplicas": count, "availableReplicas": count},
		}
		if mutate != nil {
			mutate(d)
		}
		items = append(items, d)
	}
	raw, _ := json.Marshal(map[string]any{"items": items})
	return raw
}

func TestObservationOnlyReadsAndKeepsIdentity(t *testing.T) {
	_, g, _ := fixture(t)
	p := intake.Plan{EnvironmentRevision: g.p.EnvironmentRevision, BackendImage: "ghcr.io/" + intake.Organization + "/backend@" + g.p.Backend.Digest, FrontendImage: "ghcr.io/" + intake.Organization + "/frontend@" + g.p.Frontend.Digest}
	ready := false
	var mutation func(map[string]any)
	var makeCalls []string
	c := Commands{Run: func(_ context.Context, _ string, _ []string, name string, args ...string) ([]byte, error) {
		if name == "kubectl" {
			return deploymentResponse(p, ready, mutation), nil
		}
		if name == "make" {
			makeCalls = append(makeCalls, args[0])
			if args[0] == "apply" || args[0] == "deploy" {
				t.Fatal("observer applied resources")
			}
			if args[0] == "wait" {
				ready = true
			}
		}
		return nil, nil
	}}
	c.dir, c.revision = t.TempDir(), p.EnvironmentRevision
	if err := c.Wait(context.Background(), p, testSnapshot(p), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := c.Smoke(context.Background(), p, testSnapshot(p)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(makeCalls, []string{"wait", "smoke"}) {
		t.Fatalf("unexpected observation commands %v", makeCalls)
	}
	for _, kind := range []string{"uid", "generation", "image", "stalled"} {
		t.Run(kind, func(t *testing.T) {
			mutation = func(d map[string]any) {
				switch kind {
				case "uid":
					d["metadata"].(map[string]any)["uid"] = "replacement"
				case "generation":
					d["metadata"].(map[string]any)["generation"] = 2
				case "image":
					d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"] = []any{map[string]any{"image": "other-image"}}
				case "stalled":
					d["status"].(map[string]any)["readyReplicas"] = 0
					d["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded"}}
				}
			}
			before := len(makeCalls)
			var failure *Failure
			if err := c.Wait(context.Background(), p, testSnapshot(p), time.Now().Add(time.Minute)); !errors.As(err, &failure) {
				t.Fatalf("identity change was not a failure: %v", err)
			}
			if len(makeCalls) != before {
				t.Fatal("ran environment command for a changed deployment")
			}
		})
	}
}

func TestUnsupportedEnvironmentAndDiagnosticsFiltering(t *testing.T) {
	p := intake.Plan{EnvironmentRevision: strings.Repeat("a", 40), BackendImage: "ghcr.io/" + intake.Organization + "/backend@sha256:" + strings.Repeat("b", 64), FrontendImage: "ghcr.io/" + intake.Organization + "/frontend@sha256:" + strings.Repeat("c", 64)}
	c := Commands{WorkDir: t.TempDir(), Run: func(_ context.Context, _ string, _ []string, name string, args ...string) ([]byte, error) {
		if name == "git" && args[0] == "rev-parse" {
			return []byte(p.EnvironmentRevision), nil
		}
		if name == "make" {
			return nil, errors.New("No rule to make target")
		}
		return nil, nil
	}}
	if err := c.Prepare(context.Background(), p); err == nil {
		t.Fatal("accepted old monolithic environment")
	}
	c.Run = func(context.Context, string, []string, string, ...string) ([]byte, error) {
		return []byte(`{"items":[{"kind":"Pod","metadata":{"name":"frontend"},"spec":{"containers":[{"env":[{"value":"must-not-log-env"}]}]},"status":{"containerStatuses":[{"name":"web","state":{"waiting":{"reason":"ImagePullBackOff"}}}]}}]}`), nil
	}
	raw, err := c.Diagnose(context.Background(), p)
	if err != nil || strings.Contains(string(raw), "must-not-log-env") || !strings.Contains(string(raw), "ImagePullBackOff") {
		t.Fatalf("diagnostics filtering failed: %v", err)
	}
}
