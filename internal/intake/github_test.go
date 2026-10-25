package intake

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestGitHubPaginationAndNewestStatus(t *testing.T) {
	g := GitHub{Run: func(_ context.Context, args ...string) ([]byte, error) {
		if !reflect.DeepEqual(args[len(args)-2:], []string{"--paginate", "--slurp"}) {
			t.Fatalf("missing pagination: %v", args)
		}
		if strings.Contains(args[3], "statuses?") {
			return []byte(`[[{"id":12,"state":"queued"}],[{"id":15,"state":"success"}]]`), nil
		}
		return []byte(`[[{"id":10}],[{"id":20},{"id":15}]]`), nil
	}}
	d, err := g.Latest(context.Background())
	if err != nil || d.ID != 20 {
		t.Fatalf("latest: %+v %v", d, err)
	}
	state, err := g.Status(context.Background(), 20)
	if err != nil || state != "success" {
		t.Fatalf("status: %s %v", state, err)
	}
}

func TestGitHubReleaseAsset(t *testing.T) {
	for _, kind := range []string{"valid", "draft", "prerelease", "wrong tag", "missing asset", "huge asset", "unknown field"} {
		t.Run(kind, func(t *testing.T) {
			g := GitHub{Run: func(_ context.Context, args ...string) ([]byte, error) {
				endpoint := args[3]
				if strings.Contains(endpoint, "/releases/assets/") {
					if endpoint != "repos/"+Organization+"/backend/releases/assets/42" {
						t.Fatalf("unexpected asset endpoint: %s", endpoint)
					}
					raw := `{"schema_version":"1.0.0"}`
					if kind == "unknown field" {
						raw = `{"schema_version":"1.0.0","extra":true}`
					}
					return []byte(raw), nil
				}
				m := map[string]any{"tag_name": "v0.1.0", "assets": []any{map[string]any{"id": 42, "name": "release.json", "size": 512}}}
				switch kind {
				case "draft":
					m["draft"] = true
				case "prerelease":
					m["prerelease"] = true
				case "wrong tag":
					m["tag_name"] = "v0.2.0"
				case "missing asset":
					m["assets"] = []any{}
				case "huge asset":
					m["assets"] = []any{map[string]any{"id": 42, "name": "release.json", "size": 65537}}
				}
				return json.Marshal(m)
			}}
			_, err := g.Release(context.Background(), "backend", "v0.1.0")
			if (err == nil) != (kind == "valid") {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
}

func TestProvenancePolicyAndFailure(t *testing.T) {
	r := Release{Repository: Organization + "/backend", Tag: "v0.1.0", Revision: strings.Repeat("a", 40), Image: "ghcr.io/" + Organization + "/backend", Digest: "sha256:" + strings.Repeat("b", 64)}
	for _, result := range []string{`[{"verified":true}]`, `[]`, `null`, `[null]`, `invalid`} {
		t.Run(result, func(t *testing.T) {
			g := GitHub{Run: func(_ context.Context, args ...string) ([]byte, error) {
				want := []string{"attestation", "verify", "oci://" + r.Image + "@" + r.Digest, "--hostname", "github.com", "--repo", r.Repository, "--source-digest", r.Revision, "--source-ref", "refs/tags/" + r.Tag, "--signer-workflow", r.Repository + "/.github/workflows/release.yml", "--deny-self-hosted-runners", "--predicate-type", "https://slsa.dev/provenance/v1", "--format", "json"}
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("policy mismatch: %v", args)
				}
				return []byte(result), nil
			}}
			err := g.Verify(context.Background(), r)
			if (err == nil) != (result == `[{"verified":true}]`) {
				t.Fatalf("unexpected verification: %v", err)
			}
		})
	}
	g := GitHub{Run: func(context.Context, ...string) ([]byte, error) { return nil, errors.New("offline") }}
	if _, err := g.Latest(context.Background()); err == nil {
		t.Fatal("network failure treated as empty queue")
	}
	if err := g.Verify(context.Background(), r); err == nil {
		t.Fatal("failed verifier treated as success")
	}
}

func TestMainAncestry(t *testing.T) {
	sha := strings.Repeat("a", 40)
	g := GitHub{Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[3] != "repos/"+Repository+"/compare/"+sha+"...main" {
			t.Fatalf("wrong comparison direction: %v", args)
		}
		return json.Marshal(sha)
	}}
	if ok, err := g.OnMain(context.Background(), sha); err != nil || !ok {
		t.Fatalf("ancestry: %v %v", ok, err)
	}
}
