package intake

import (
	"context"
	"reflect"
	"testing"
)

func TestWriteStatus(t *testing.T) {
	calls := 0
	g := GitHub{Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		want := []string{"api", "--hostname", "github.com", "--method", "POST", "repos/" + Repository + "/deployments/42/statuses", "-f", "state=success", "-f", "description=Rollouts and Gateway smoke passed", "-F", "auto_inactive=false"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("unexpected status: %v", args)
		}
		return []byte(`{}`), nil
	}}
	if err := g.WriteStatus(context.Background(), 42, "success"); err != nil {
		t.Fatal(err)
	}
	if err := g.WriteStatus(context.Background(), 42, "queued"); err == nil {
		t.Fatal("allowed unexpected state")
	}
	if err := g.WriteStatus(context.Background(), 0, "success"); err == nil {
		t.Fatal("allowed invalid ID")
	}
	if calls != 1 {
		t.Fatal("invalid state was sent")
	}
}
