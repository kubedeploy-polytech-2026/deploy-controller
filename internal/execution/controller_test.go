package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kubedeploy/deploy-controller/internal/intake"
)

type fakeGitHub struct {
	d           intake.Deployment
	p           intake.Payload
	state       string
	writes      []string
	failWrite   string
	latestCalls int
	supersede   bool
}

func (g *fakeGitHub) Latest(context.Context) (*intake.Deployment, error) {
	g.latestCalls++
	d := g.d
	if g.supersede && g.latestCalls >= 3 {
		d.ID++
	}
	return &d, nil
}
func (g *fakeGitHub) Status(context.Context, int64) (string, error) { return g.state, nil }
func (g *fakeGitHub) OnMain(context.Context, string) (bool, error)  { return true, nil }
func (g *fakeGitHub) Release(_ context.Context, c, tag string) (intake.Release, error) {
	return intake.Release{SchemaVersion: "1.0.0", Repository: intake.Organization + "/" + c, Tag: tag, Revision: g.p.Backend.Revision, Image: "ghcr.io/" + intake.Organization + "/" + c, Digest: g.p.Backend.Digest, Platforms: []string{"linux/amd64", "linux/arm64"}}, nil
}
func (g *fakeGitHub) TagRevision(context.Context, string, string) (string, error) {
	return g.p.Backend.Revision, nil
}
func (g *fakeGitHub) Verify(context.Context, intake.Release) error { return nil }
func (g *fakeGitHub) WriteStatus(_ context.Context, id int64, state string, reason ...string) error {
	if id != g.d.ID {
		return errors.New("wrong deployment")
	}
	g.writes = append(g.writes, state)
	if state == g.failWrite {
		return errors.New("GitHub unavailable")
	}
	g.state = state
	return nil
}

type fakeExecutor struct {
	readyErr, prepareErr, applyErr, waitErr, smokeErr error
	waitCalls, smokeCalls                             int
	waitFn                                            func(context.Context) error
	smokeFn                                           func(context.Context) error
	calls                                             int
	plan                                              intake.Plan
}

func (e *fakeExecutor) Ready(context.Context) error                { return e.readyErr }
func (e *fakeExecutor) Prepare(context.Context, intake.Plan) error { return e.prepareErr }
func (e *fakeExecutor) Wait(ctx context.Context, _ intake.Plan, _ Snapshot, _ time.Time) error {
	e.waitCalls++
	if e.waitFn != nil {
		return e.waitFn(ctx)
	}
	return e.waitErr
}
func (e *fakeExecutor) Smoke(ctx context.Context, _ intake.Plan, _ Snapshot) error {
	e.smokeCalls++
	if e.smokeFn != nil {
		return e.smokeFn(ctx)
	}
	return e.smokeErr
}
func (e *fakeExecutor) Diagnose(context.Context, intake.Plan) ([]byte, error) {
	return []byte(`{"items":[]}`), nil
}
func (e *fakeExecutor) Apply(_ context.Context, p intake.Plan) (Snapshot, error) {
	e.calls++
	e.plan = p
	return testSnapshot(p), e.applyErr
}

func fixture(t *testing.T) (Controller, *fakeGitHub, *fakeExecutor) {
	t.Helper()
	now := time.Now().UTC()
	s := intake.Selection{Tag: "v0.1.0", Revision: strings.Repeat("b", 40), Digest: "sha256:" + strings.Repeat("c", 64)}
	p := intake.Payload{SchemaVersion: "1.0.0", EnvironmentRevision: strings.Repeat("a", 40), ExpiresAt: now.Add(time.Hour), Backend: s, Frontend: s}
	raw, _ := json.Marshal(p)
	g := &fakeGitHub{d: intake.Deployment{ID: 42, SHA: p.EnvironmentRevision, Task: "deploy", Environment: intake.Environment, CreatedAt: now.Add(-time.Minute), Payload: raw}, p: p, state: "queued"}
	e := &fakeExecutor{}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return Controller{GitHub: g, Executor: e, Store: store, Now: func() time.Time { return now }}, g, e
}

func TestExecutionAndNoRepeat(t *testing.T) {
	c, g, e := fixture(t)
	p, err := c.Step(context.Background())
	if err != nil || p.State != "success" {
		t.Fatalf("execute: %+v %v", p, err)
	}
	if e.calls != 1 || e.plan.EnvironmentRevision != g.p.EnvironmentRevision || !reflect.DeepEqual(g.writes, []string{"in_progress", "success"}) {
		t.Fatalf("calls=%d writes=%v plan=%+v", e.calls, g.writes, e.plan)
	}
	for _, state := range []string{"success", "queued"} {
		g.state = state
		if _, err = c.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
		if e.calls != 1 {
			t.Fatal("repeated completed apply")
		}
	}
	g.d.ID--
	if _, err = c.Step(context.Background()); err != nil || e.calls != 1 {
		t.Fatalf("replayed an older request: calls=%d err=%v", e.calls, err)
	}
	j, err := c.Store.Load()
	if err != nil || !j.Reported || j.State != "success" {
		t.Fatalf("journal %+v %v", j, err)
	}
}

func TestOfflineAndSupersededStayUnapplied(t *testing.T) {
	for _, kind := range []string{"offline", "superseded"} {
		t.Run(kind, func(t *testing.T) {
			c, g, e := fixture(t)
			if kind == "offline" {
				e.readyErr = errors.New("cluster offline")
			} else {
				g.supersede = true
			}
			if _, err := c.Step(context.Background()); err == nil {
				t.Fatal("expected error")
			}
			if e.calls != 0 || len(g.writes) != 0 || g.state != "queued" {
				t.Fatalf("mutated unavailable request: calls=%d writes=%v", e.calls, g.writes)
			}
			if j, err := c.Store.Load(); err != nil || j != nil {
				t.Fatalf("unexpected journal: %+v %v", j, err)
			}
		})
	}
}

func TestFailureAndStatusRetryNeverReapply(t *testing.T) {
	for _, failApply := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failApply], func(t *testing.T) {
			c, g, e := fixture(t)
			want := "success"
			if failApply {
				e.waitErr = &Failure{Reason: "rollout stopped progressing"}
				want = "failure"
			}
			g.failWrite = want
			if _, err := c.Step(context.Background()); err == nil {
				t.Fatal("status write failure lost")
			}
			j, err := c.Store.Load()
			if err != nil || j.State != want || j.Reported {
				t.Fatalf("missing pending status %+v %v", j, err)
			}
			g.failWrite = ""
			p, err := c.Step(context.Background())
			if err != nil || p.State != want || e.calls != 1 || g.state != want {
				t.Fatalf("retry %+v err=%v calls=%d state=%s", p, err, e.calls, g.state)
			}
		})
	}
}

func TestInterruptedExecutionReportsError(t *testing.T) {
	c, g, e := fixture(t)
	j := Journal{Plan: intake.Plan{DeploymentID: g.d.ID}, State: "in_progress"}
	if err := c.Store.Save(j); err != nil {
		t.Fatal(err)
	}
	p, err := c.Step(context.Background())
	if err != nil || p.State != "error" || e.calls != 0 || !reflect.DeepEqual(g.writes, []string{"error"}) {
		t.Fatalf("interrupted result %+v %v calls=%d writes=%v", p, err, e.calls, g.writes)
	}
}

func TestClaimFailureDoesNotApply(t *testing.T) {
	c, g, e := fixture(t)
	g.failWrite = "in_progress"
	if _, err := c.Step(context.Background()); err == nil {
		t.Fatal("claim failure lost")
	}
	if e.calls != 0 {
		t.Fatal("applied without confirmed in_progress")
	}
	g.failWrite = ""
	p, err := c.Step(context.Background())
	if err != nil || p.State != "error" || e.calls != 0 {
		t.Fatalf("claim retry %+v %v", p, err)
	}
}

func TestStateLockAndCorruption(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Open(dir); err == nil {
		second.Close()
		t.Fatal("second controller acquired lock")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = os.WriteFile(filepath.Join(dir, "deployment.json"), []byte(`{"state":"garbage"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(); err == nil {
		t.Fatal("corrupt journal ignored")
	}
}

func testSnapshot(p intake.Plan) Snapshot {
	return Snapshot{"backend": {UID: "backend-uid", Generation: 1, Image: p.BackendImage}, "frontend": {UID: "frontend-uid", Generation: 1, Image: p.FrontendImage}}
}

func TestObservationResumesAfterRestartWithoutApply(t *testing.T) {
	c, g, e := fixture(t)
	e.waitErr = errors.New("temporary Kubernetes API failure")
	if p, err := c.Step(context.Background()); err == nil || p.State != "in_progress" {
		t.Fatalf("expected resumable observation: %+v %v", p, err)
	}
	j, err := c.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	deadline, phaseDeadline := j.Deadline, j.PhaseDeadline
	expired := c.Now().Add(-time.Minute)
	j.Plan.ExpiresAt = &expired
	if err = c.Store.Save(*j); err != nil {
		t.Fatal(err)
	}
	dir := c.Store.dir
	c.Store.Close()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c.Store = store
	resumed := &fakeExecutor{waitFn: func(ctx context.Context) error {
		actual, _ := ctx.Deadline()
		if !actual.Equal(phaseDeadline) {
			t.Fatal("restart reset the rollout deadline")
		}
		return nil
	}}
	c.Executor = resumed
	c.Budgets = Budgets{Total: time.Minute, Apply: time.Minute, Rollout: time.Minute, Smoke: time.Minute}
	p, err := c.Step(context.Background())
	if err != nil || p.State != "success" || resumed.calls != 0 || e.calls != 1 {
		t.Fatalf("resume: %+v %v original=%d resumed=%d", p, err, e.calls, resumed.calls)
	}
	saved, _ := c.Store.Load()
	if !saved.Deadline.Equal(deadline) || !reflect.DeepEqual(g.writes, []string{"in_progress", "success"}) {
		t.Fatal("restart changed deadline or reapplied status")
	}
}

func TestSmokeResumesWithoutRepeatingRollout(t *testing.T) {
	c, _, e := fixture(t)
	e.smokeErr = errors.New("temporary HTTP failure")
	if _, err := c.Step(context.Background()); err == nil {
		t.Fatal("expected smoke retry")
	}
	j, _ := c.Store.Load()
	if j.Phase != "smoke" {
		t.Fatal("smoke checkpoint not persisted")
	}
	resumed := &fakeExecutor{}
	c.Executor = resumed
	if p, err := c.Step(context.Background()); err != nil || p.State != "success" {
		t.Fatalf("smoke resume %+v %v", p, err)
	}
	if resumed.calls != 0 || resumed.waitCalls != 0 || resumed.smokeCalls != 1 {
		t.Fatal("smoke recovery repeated apply or rollout")
	}
}

func TestDeadlineReportsErrorWithDiagnostics(t *testing.T) {
	for _, phase := range []string{"waiting", "smoke"} {
		t.Run(phase, func(t *testing.T) {
			c, g, e := fixture(t)
			clock := c.Now()
			c.Now = func() time.Time { return clock }
			timeout := func(context.Context) error { clock = clock.Add(11 * time.Minute); return context.DeadlineExceeded }
			want := "rollout timeout"
			if phase == "waiting" {
				e.waitFn = timeout
			} else {
				e.smokeFn = timeout
				want = "smoke timeout"
			}
			p, err := c.Step(context.Background())
			if err != nil || p.State != "error" || p.Reason != want || g.state != "error" {
				t.Fatalf("timeout result %+v %v", p, err)
			}
			raw, err := os.ReadFile(filepath.Join(c.Store.dir, "diagnostics-42.json"))
			if err != nil {
				t.Fatal(err)
			}
			var diag map[string]any
			if err = json.Unmarshal(raw, &diag); err != nil || diag["reason"] != want || diag["expected"] == nil {
				t.Fatal("missing timeout diagnostics")
			}
			before := e.calls
			if _, err = c.Step(context.Background()); err != nil || e.calls != before {
				t.Fatal("deadline triggered another apply")
			}
		})
	}
}

func TestCancellationPreservesObservation(t *testing.T) {
	c, g, e := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.waitFn = func(context.Context) error { cancel(); return context.Canceled }
	if _, err := c.Step(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
	j, _ := c.Store.Load()
	if j.Phase != "waiting" || j.State != "in_progress" || j.Reported || g.state != "in_progress" {
		t.Fatal("cancellation destroyed observation checkpoint")
	}
	c.Executor = &fakeExecutor{}
	if p, err := c.Step(context.Background()); err != nil || p.State != "success" {
		t.Fatalf("cancelled observer did not recover: %+v %v", p, err)
	}
}

func TestExternalCancellationDoesNotGetOverwritten(t *testing.T) {
	c, g, e := fixture(t)
	e.waitErr = errors.New("temporary read failure")
	if _, err := c.Step(context.Background()); err == nil {
		t.Fatal("expected retry")
	}
	g.state = "inactive"
	if p, err := c.Step(context.Background()); err != nil || p.State != "error" {
		t.Fatalf("external cancel %+v %v", p, err)
	}
	if g.state != "inactive" || !reflect.DeepEqual(g.writes, []string{"in_progress"}) {
		t.Fatal("overwrote external cancellation")
	}
}

func TestUnknownApplyOutcomeNeverRepeats(t *testing.T) {
	c, g, e := fixture(t)
	e.applyErr = errors.New("connection lost during apply")
	p, err := c.Step(context.Background())
	if err != nil || p.State != "error" || g.state != "error" || e.waitCalls != 0 {
		t.Fatalf("ambiguous apply %+v %v", p, err)
	}
	if _, err = c.Step(context.Background()); err != nil || e.calls != 1 {
		t.Fatal("repeated ambiguous apply")
	}
}

func TestPreflightAndInvalidJournalStayFailClosed(t *testing.T) {
	c, g, e := fixture(t)
	e.prepareErr = errors.New("environment contract unavailable")
	if _, err := c.Step(context.Background()); err == nil || e.calls != 0 || len(g.writes) != 0 {
		t.Fatal("preflight changed queued request")
	}
	p := intake.Plan{DeploymentID: 42, EnvironmentRevision: g.p.EnvironmentRevision, BackendImage: "ghcr.io/" + intake.Organization + "/backend@" + g.p.Backend.Digest, FrontendImage: "ghcr.io/" + intake.Organization + "/frontend@" + g.p.Frontend.Digest}
	j := Journal{Plan: p, State: "in_progress", Phase: "waiting", Deadline: time.Now().Add(time.Minute), PhaseDeadline: time.Now().Add(time.Hour), Snapshot: testSnapshot(p)}
	c.Store.Save(j)
	if _, err := c.Store.Load(); err == nil {
		t.Fatal("accepted phase deadline past overall deadline")
	}
	j.PhaseDeadline = j.Deadline
	j.Snapshot["backend"] = Identity{UID: "wrong", Generation: 1, Image: "untrusted"}
	c.Store.Save(j)
	if _, err := c.Store.Load(); err == nil {
		t.Fatal("accepted untrusted saved snapshot")
	}
}
