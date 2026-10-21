package intake

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func fixture() (Deployment, Payload) {
	s := Selection{Tag: "v0.1.0", Revision: strings.Repeat("b", 40), Digest: "sha256:" + strings.Repeat("c", 64)}
	p := Payload{SchemaVersion: "1.0.0", EnvironmentRevision: strings.Repeat("a", 40), ExpiresAt: testNow.Add(time.Hour), Backend: s, Frontend: s}
	raw, _ := json.Marshal(p)
	return Deployment{ID: 10, SHA: p.EnvironmentRevision, Task: "deploy", Environment: Environment, CreatedAt: testNow.Add(-time.Minute), Payload: raw}, p
}

func TestRequestValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Deployment, *Payload)
	}{
		{"foreign environment", func(d *Deployment, p *Payload) { d.Environment = "production" }},
		{"production flag", func(d *Deployment, p *Payload) { d.Production = true }},
		{"wrong task", func(d *Deployment, p *Payload) { d.Task = "destroy" }},
		{"missing id", func(d *Deployment, p *Payload) { d.ID = 0 }},
		{"wrong schema", func(d *Deployment, p *Payload) { p.SchemaVersion = "2.0.0" }},
		{"revision mismatch", func(d *Deployment, p *Payload) { d.SHA = strings.Repeat("f", 40) }},
		{"short revision", func(d *Deployment, p *Payload) { p.EnvironmentRevision = "aaaaaaa"; d.SHA = p.EnvironmentRevision }},
		{"expired at boundary", func(d *Deployment, p *Payload) { p.ExpiresAt = testNow }},
		{"TTL too long", func(d *Deployment, p *Payload) { p.ExpiresAt = d.CreatedAt.Add(24*time.Hour + time.Second) }},
		{"future request", func(d *Deployment, p *Payload) { d.CreatedAt = testNow.Add(6 * time.Minute) }},
		{"missing creation", func(d *Deployment, p *Payload) { d.CreatedAt = time.Time{} }},
		{"expiry before creation", func(d *Deployment, p *Payload) { p.ExpiresAt = d.CreatedAt }},
		{"floating tag", func(d *Deployment, p *Payload) { p.Backend.Tag = "latest" }},
		{"tag traversal", func(d *Deployment, p *Payload) { p.Backend.Tag = "../../main" }},
		{"leading zero", func(d *Deployment, p *Payload) { p.Frontend.Tag = "v01.2.3" }},
		{"short app revision", func(d *Deployment, p *Payload) { p.Frontend.Revision = "bbbbbbb" }},
		{"invalid digest", func(d *Deployment, p *Payload) { p.Frontend.Digest = "sha256:abc" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, p := fixture()
			tc.mutate(&d, &p)
			d.Payload, _ = json.Marshal(p)
			if _, err := d.Validate(testNow); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	d, _ := fixture()
	if _, err := d.Validate(testNow); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`null`, `{}`, `"payload"`, string(d.Payload) + ` {}`, strings.TrimSuffix(string(d.Payload), "}") + `,"unexpected":true}`} {
		d.Payload = json.RawMessage(raw)
		if _, err := d.Validate(testNow); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

type fakeSource struct {
	d             *Deployment
	p             Payload
	onMain        bool
	state         string
	latestCalls   int
	statusCalls   int
	verifyCalls   int
	mutateRelease func(*Release)
	tagMoved      bool
	verifyErr     error
	supersede     bool
	changeStatus  bool
}

func newFake() *fakeSource {
	d, p := fixture()
	return &fakeSource{d: &d, p: p, onMain: true, state: "queued"}
}
func (f *fakeSource) Latest(context.Context) (*Deployment, error) {
	f.latestCalls++
	if f.supersede && f.latestCalls > 1 {
		d := *f.d
		d.ID++
		return &d, nil
	}
	return f.d, nil
}
func (f *fakeSource) Status(context.Context, int64) (string, error) {
	f.statusCalls++
	if f.changeStatus && f.statusCalls > 1 {
		return "inactive", nil
	}
	return f.state, nil
}
func (f *fakeSource) OnMain(context.Context, string) (bool, error) { return f.onMain, nil }
func (f *fakeSource) Release(_ context.Context, c, tag string) (Release, error) {
	s := f.p.Backend
	if c == "frontend" {
		s = f.p.Frontend
	}
	r := Release{SchemaVersion: "1.0.0", Repository: Organization + "/" + c, Tag: tag, Revision: s.Revision, Image: "ghcr.io/" + Organization + "/" + c, Digest: s.Digest, Platforms: []string{"linux/amd64", "linux/arm64"}}
	if f.mutateRelease != nil {
		f.mutateRelease(&r)
	}
	return r, nil
}
func (f *fakeSource) TagRevision(_ context.Context, component, _ string) (string, error) {
	if f.tagMoved {
		return strings.Repeat("d", 40), nil
	}
	if component == "frontend" {
		return f.p.Frontend.Revision, nil
	}
	return f.p.Backend.Revision, nil
}
func (f *fakeSource) Verify(context.Context, Release) error { f.verifyCalls++; return f.verifyErr }

func TestPoll(t *testing.T) {
	f := newFake()
	f.p.Frontend = Selection{Tag: "v0.2.0", Revision: strings.Repeat("e", 40), Digest: "sha256:" + strings.Repeat("f", 64)}
	f.d.Payload, _ = json.Marshal(f.p)
	plan, err := Poll(context.Background(), f, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != "validated" || plan.DeploymentID != 10 || f.verifyCalls != 2 || plan.BackendImage != "ghcr.io/"+Organization+"/backend@"+f.p.Backend.Digest || plan.FrontendImage != "ghcr.io/"+Organization+"/frontend@"+f.p.Frontend.Digest {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	f = newFake()
	f.p.ExpiresAt = testNow
	f.d.Payload, _ = json.Marshal(f.p)
	if _, err := Poll(context.Background(), f, func() time.Time { return testNow }); err == nil || f.verifyCalls != 0 {
		t.Fatal("expired latest request must stop without verifying releases or reviving older queue")
	}
	for _, state := range []string{"", "pending", "success", "failure", "inactive", "in_progress"} {
		t.Run(state, func(t *testing.T) {
			f := newFake()
			f.state = state
			plan, err := Poll(context.Background(), f, func() time.Time { return testNow })
			if err != nil || plan.State != "idle" || f.verifyCalls != 0 {
				t.Fatalf("terminal request should not revive older queue: %+v %v", plan, err)
			}
		})
	}
	f = newFake()
	f.d = nil
	if plan, err := Poll(context.Background(), f, time.Now); err != nil || plan.State != "idle" {
		t.Fatalf("empty queue: %+v %v", plan, err)
	}
}

func TestPollRejectsUntrustedAndChangedRequests(t *testing.T) {
	cases := []struct {
		name   string
		change func(*fakeSource)
	}{
		{"unmerged environment", func(f *fakeSource) { f.onMain = false }},
		{"foreign repository", func(f *fakeSource) { f.mutateRelease = func(r *Release) { r.Repository = "attacker/backend" } }},
		{"foreign registry", func(f *fakeSource) { f.mutateRelease = func(r *Release) { r.Image = "ghcr.io/attacker/backend" } }},
		{"digest mismatch", func(f *fakeSource) {
			f.mutateRelease = func(r *Release) { r.Digest = "sha256:" + strings.Repeat("d", 64) }
		}},
		{"revision mismatch", func(f *fakeSource) { f.mutateRelease = func(r *Release) { r.Revision = strings.Repeat("d", 40) } }},
		{"tag mismatch", func(f *fakeSource) { f.mutateRelease = func(r *Release) { r.Tag = "v0.2.0" } }},
		{"wrong release schema", func(f *fakeSource) { f.mutateRelease = func(r *Release) { r.SchemaVersion = "2.0.0" } }},
		{"missing platform", func(f *fakeSource) { f.mutateRelease = func(r *Release) { r.Platforms = r.Platforms[:1] } }},
		{"duplicate platform", func(f *fakeSource) {
			f.mutateRelease = func(r *Release) { r.Platforms = []string{"linux/amd64", "linux/amd64"} }
		}},
		{"moved tag", func(f *fakeSource) { f.tagMoved = true }},
		{"invalid signature", func(f *fakeSource) { f.verifyErr = errors.New("invalid signature") }},
		{"newer request", func(f *fakeSource) { f.supersede = true }},
		{"status changed", func(f *fakeSource) { f.changeStatus = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			tc.change(f)
			if _, err := Poll(context.Background(), f, func() time.Time { return testNow }); err == nil {
				t.Fatal("accepted unsafe plan")
			}
		})
	}
	f := newFake()
	calls := 0
	if _, err := Poll(context.Background(), f, func() time.Time {
		calls++
		if calls > 1 {
			return testNow.Add(2 * time.Hour)
		}
		return testNow
	}); err == nil {
		t.Fatal("accepted request that expired during verification")
	}
}
