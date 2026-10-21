package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Run executes gh directly, never through a shell. The CLI owns credentials and
// signature verification; tokens do not appear in command arguments or plans.
type Run func(context.Context, ...string) ([]byte, error)

type limitedBuffer struct{ buffer bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 4<<20 {
		return 0, fmt.Errorf("gh output exceeds 4 MiB")
	}
	return b.buffer.Write(p)
}

func RunGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = append(os.Environ(), "GH_HOST=github.com", "GH_PROMPT_DISABLED=1", "GH_PAGER=cat")
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// Do not include CLI output: it may contain repository-controlled content.
		return nil, fmt.Errorf("gh %s failed: %w", args[0], err)
	}
	return stdout.buffer.Bytes(), nil
}

type GitHub struct{ Run Run }

func (g GitHub) api(ctx context.Context, endpoint string, out any, extra ...string) error {
	args := append([]string{"api", "--hostname", "github.com", endpoint}, extra...)
	raw, err := g.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("GitHub %s: %w", endpoint, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode GitHub API response: %w", err)
	}
	return nil
}

func (g GitHub) Latest(ctx context.Context) (*Deployment, error) {
	var pages [][]Deployment
	err := g.api(ctx, "repos/"+Repository+"/deployments?environment="+Environment+"&per_page=100", &pages, "--paginate", "--slurp")
	if err != nil {
		return nil, err
	}
	var latest *Deployment
	for _, page := range pages {
		for _, d := range page {
			if latest == nil || d.ID > latest.ID {
				copy := d
				latest = &copy
			}
		}
	}
	return latest, nil
}

func (g GitHub) Status(ctx context.Context, id int64) (string, error) {
	var pages [][]struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
	}
	err := g.api(ctx, "repos/"+Repository+"/deployments/"+strconv.FormatInt(id, 10)+"/statuses?per_page=100", &pages, "--paginate", "--slurp")
	if err != nil {
		return "", err
	}
	var newest int64
	var state string
	for _, page := range pages {
		for _, status := range page {
			if status.ID > newest {
				newest, state = status.ID, status.State
			}
		}
	}
	return state, nil
}

func (g GitHub) OnMain(ctx context.Context, revision string) (bool, error) {
	var base string
	err := g.api(ctx, "repos/"+Repository+"/compare/"+revision+"...main", &base, "--jq", ".merge_base_commit.sha | tojson")
	return base == revision, err
}

func (g GitHub) TagRevision(ctx context.Context, component, tag string) (string, error) {
	var commit struct {
		SHA string `json:"sha"`
	}
	err := g.api(ctx, "repos/"+Organization+"/"+component+"/commits/"+tag, &commit)
	return commit.SHA, err
}

func (g GitHub) Release(ctx context.Context, component, tag string) (Release, error) {
	repo := Organization + "/" + component
	var published struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := g.api(ctx, "repos/"+repo+"/releases/tags/"+tag, &published); err != nil {
		return Release{}, err
	}
	if published.Tag != tag || published.Draft || published.Prerelease {
		return Release{}, fmt.Errorf("expected a published stable release")
	}
	var assetID int64
	for _, a := range published.Assets {
		if a.Name == "release.json" {
			if assetID != 0 || a.ID <= 0 || a.Size <= 0 || a.Size > 65536 {
				return Release{}, fmt.Errorf("invalid release.json asset")
			}
			assetID = a.ID
		}
	}
	if assetID == 0 {
		return Release{}, fmt.Errorf("release.json asset is missing")
	}
	raw, err := g.Run(ctx, "api", "--hostname", "github.com", "repos/"+repo+"/releases/assets/"+strconv.FormatInt(assetID, 10), "-H", "Accept: application/octet-stream")
	if err != nil {
		return Release{}, err
	}
	var r Release
	if err := decodeStrict(raw, &r); err != nil {
		return r, fmt.Errorf("release.json: %w", err)
	}
	return r, nil
}

func (g GitHub) Verify(ctx context.Context, r Release) error {
	raw, err := g.Run(ctx, "attestation", "verify", "oci://"+r.Image+"@"+r.Digest,
		"--hostname", "github.com", "--repo", r.Repository,
		"--source-digest", r.Revision, "--source-ref", "refs/tags/"+r.Tag,
		"--signer-workflow", r.Repository+"/.github/workflows/release.yml",
		"--deny-self-hosted-runners", "--predicate-type", "https://slsa.dev/provenance/v1", "--format", "json")
	if err != nil {
		return err
	}
	var results []json.RawMessage
	if err := json.Unmarshal(raw, &results); err != nil {
		return fmt.Errorf("decode provenance result: %w", err)
	}
	if len(results) == 0 || strings.TrimSpace(string(results[0])) == "null" {
		return fmt.Errorf("no verified provenance")
	}
	return nil
}
