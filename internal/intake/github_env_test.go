package intake

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGHOverridesOnlyChildEnvironmentAndHidesErrors(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$GH_TOKEN" != "app-token" ] || [ -n "$GITHUB_TOKEN" ] || [ -n "$GH_DEBUG" ]; then exit 2; fi
if [ "$1" = fail ]; then echo secret-must-not-leak >&2; exit 1; fi
printf '{"ok":true}'
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", "personal-token")
	t.Setenv("GITHUB_TOKEN", "another-personal-token")
	t.Setenv("GH_DEBUG", "api")
	env := []string{"GH_TOKEN=app-token", "GITHUB_TOKEN=", "GH_DEBUG="}
	if out, err := RunGHWithEnv(context.Background(), env, "api"); err != nil || string(out) != `{"ok":true}` {
		t.Fatalf("child environment did not override inherited auth: %v", err)
	}
	if os.Getenv("GH_TOKEN") != "personal-token" || os.Getenv("GH_DEBUG") != "api" {
		t.Fatal("process-wide environment changed")
	}
	if _, err := RunGHWithEnv(context.Background(), env, "fail"); err == nil || strings.Contains(err.Error(), "secret-must-not-leak") {
		t.Fatal("child error output leaked")
	}
}
