// Package intake produces a read-only deployment plan from the latest GitHub request.
package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

const (
	Organization = "kubedeploy-polytech-2026"
	Repository   = Organization + "/environment"
	Environment  = "local-k3d"
)

var (
	shaPattern    = regexp.MustCompile(`^[a-f0-9]{40}$`)
	digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	tagPattern    = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

type Selection struct {
	Tag      string `json:"tag"`
	Revision string `json:"revision"`
	Digest   string `json:"digest"`
}

type Payload struct {
	SchemaVersion       string    `json:"schema_version"`
	EnvironmentRevision string    `json:"environment_revision"`
	ExpiresAt           time.Time `json:"expires_at"`
	Backend             Selection `json:"backend"`
	Frontend            Selection `json:"frontend"`
}

type Deployment struct {
	ID          int64           `json:"id"`
	SHA         string          `json:"sha"`
	Task        string          `json:"task"`
	Environment string          `json:"environment"`
	Production  bool            `json:"production_environment"`
	CreatedAt   time.Time       `json:"created_at"`
	Payload     json.RawMessage `json:"payload"`
}

type Release struct {
	SchemaVersion string   `json:"schema_version"`
	Repository    string   `json:"repository"`
	Tag           string   `json:"tag"`
	Revision      string   `json:"revision"`
	Image         string   `json:"image"`
	Digest        string   `json:"digest"`
	Platforms     []string `json:"platforms"`
}

type Plan struct {
	State               string     `json:"state"`
	Phase               string     `json:"phase,omitempty"`
	Reason              string     `json:"reason,omitempty"`
	DeploymentID        int64      `json:"deployment_id,omitempty"`
	EnvironmentRevision string     `json:"environment_revision,omitempty"`
	BackendImage        string     `json:"backend_image,omitempty"`
	FrontendImage       string     `json:"frontend_image,omitempty"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
}

// Source has no operations that mutate GitHub or Kubernetes.
type Source interface {
	Latest(context.Context) (*Deployment, error)
	Status(context.Context, int64) (string, error)
	OnMain(context.Context, string) (bool, error)
	Release(context.Context, string, string) (Release, error)
	TagRevision(context.Context, string, string) (string, error)
	Verify(context.Context, Release) error
}

func decodeStrict(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func (d Deployment) Validate(now time.Time) (Payload, error) {
	var p Payload
	if d.ID <= 0 || d.Task != "deploy" || d.Environment != Environment || d.Production {
		return p, fmt.Errorf("unexpected deployment identity or environment")
	}
	if err := decodeStrict(d.Payload, &p); err != nil {
		return p, fmt.Errorf("payload: %w", err)
	}
	if p.SchemaVersion != "1.0.0" || !shaPattern.MatchString(p.EnvironmentRevision) || p.EnvironmentRevision != d.SHA {
		return p, fmt.Errorf("payload schema or environment revision is invalid")
	}
	if d.CreatedAt.IsZero() || d.CreatedAt.After(now.Add(5*time.Minute)) || !p.ExpiresAt.After(d.CreatedAt) ||
		p.ExpiresAt.After(d.CreatedAt.Add(24*time.Hour)) || !now.Before(p.ExpiresAt) {
		return p, fmt.Errorf("request expired or TTL is outside (0, 24h]")
	}
	for _, selected := range []Selection{p.Backend, p.Frontend} {
		if !tagPattern.MatchString(selected.Tag) || !shaPattern.MatchString(selected.Revision) || !digestPattern.MatchString(selected.Digest) {
			return p, fmt.Errorf("release selection requires vMAJOR.MINOR.PATCH, full revision and sha256 digest")
		}
	}
	return p, nil
}

func validateRelease(r Release, component string, s Selection) error {
	repository := Organization + "/" + component
	if r.SchemaVersion != "1.0.0" || r.Repository != repository || r.Tag != s.Tag || r.Revision != s.Revision ||
		r.Image != "ghcr.io/"+repository || r.Digest != s.Digest {
		return fmt.Errorf("%s release.json does not match the pinned selection", component)
	}
	if len(r.Platforms) != 2 || !((r.Platforms[0] == "linux/amd64" && r.Platforms[1] == "linux/arm64") ||
		(r.Platforms[1] == "linux/amd64" && r.Platforms[0] == "linux/arm64")) {
		return fmt.Errorf("%s release must declare exactly linux/amd64 and linux/arm64", component)
	}
	return nil
}

// Poll only considers the newest request. Older queued requests are superseded,
// including when the newest request is terminal, malformed, or expired.
func Poll(ctx context.Context, source Source, now func() time.Time) (Plan, error) {
	d, err := source.Latest(ctx)
	if err != nil {
		return Plan{}, err
	}
	if d == nil {
		return Plan{State: "idle"}, nil
	}
	state, err := source.Status(ctx, d.ID)
	if err != nil {
		return Plan{}, err
	}
	if state != "queued" {
		return Plan{State: "idle"}, nil
	}
	p, err := d.Validate(now())
	if err != nil {
		return Plan{}, err
	}
	ok, err := source.OnMain(ctx, p.EnvironmentRevision)
	if err != nil {
		return Plan{}, err
	}
	if !ok {
		return Plan{}, fmt.Errorf("environment revision is not reachable from main")
	}
	images := make([]string, 0, 2)
	for i, component := range []string{"backend", "frontend"} {
		selection := []Selection{p.Backend, p.Frontend}[i]
		r, err := source.Release(ctx, component, selection.Tag)
		if err != nil {
			return Plan{}, fmt.Errorf("%s: %w", component, err)
		}
		if err := validateRelease(r, component, selection); err != nil {
			return Plan{}, err
		}
		revision, err := source.TagRevision(ctx, component, selection.Tag)
		if err != nil {
			return Plan{}, err
		}
		if revision != r.Revision {
			return Plan{}, fmt.Errorf("%s tag moved from release revision", component)
		}
		if err := source.Verify(ctx, r); err != nil {
			return Plan{}, fmt.Errorf("%s provenance: %w", component, err)
		}
		images = append(images, r.Image+"@"+r.Digest)
	}
	// Discard the plan if another request arrived, its status changed, or its TTL
	// elapsed during remote verification. Applying resources is a later stage.
	latest, err := source.Latest(ctx)
	if err != nil {
		return Plan{}, err
	}
	if latest == nil || latest.ID != d.ID {
		return Plan{}, fmt.Errorf("request was superseded during verification")
	}
	state, err = source.Status(ctx, d.ID)
	if err != nil {
		return Plan{}, err
	}
	if state != "queued" {
		return Plan{}, fmt.Errorf("request is no longer queued")
	}
	if _, err := d.Validate(now()); err != nil {
		return Plan{}, err
	}
	return Plan{State: "validated", DeploymentID: d.ID, EnvironmentRevision: p.EnvironmentRevision,
		BackendImage: images[0], FrontendImage: images[1], ExpiresAt: &p.ExpiresAt}, nil
}
