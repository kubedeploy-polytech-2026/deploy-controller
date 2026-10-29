package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"kubedeploy/deploy-controller/internal/intake"
)

type GitHub interface {
	intake.Source
	WriteStatus(context.Context, int64, string, ...string) error
}

type Executor interface {
	Ready(context.Context) error
	Prepare(context.Context, intake.Plan) error
	Apply(context.Context, intake.Plan) (Snapshot, error)
	Wait(context.Context, intake.Plan, Snapshot, time.Time) error
	Smoke(context.Context, intake.Plan, Snapshot) error
	Diagnose(context.Context, intake.Plan) ([]byte, error)
}

type Budgets struct{ Total, Apply, Rollout, Smoke time.Duration }

func DefaultBudgets() Budgets {
	return Budgets{Total: 20 * time.Minute, Apply: 2 * time.Minute, Rollout: 10 * time.Minute, Smoke: 2 * time.Minute}
}
func (b Budgets) Validate() error {
	for _, duration := range []time.Duration{b.Total, b.Apply, b.Rollout, b.Smoke} {
		if duration <= 0 || duration > 24*time.Hour {
			return fmt.Errorf("timeouts must be positive and at most 24h")
		}
	}
	if b.Apply > b.Total || b.Rollout > b.Total || b.Smoke > b.Total || b.Rollout > time.Hour {
		return fmt.Errorf("phase timeout exceeds the deployment budget or rollout limit (1h)")
	}
	return nil
}

type Controller struct {
	GitHub   GitHub
	Executor Executor
	Store    *Store
	Now      func() time.Time
	Budgets  Budgets
}

func (c Controller) budgets() Budgets {
	if c.Budgets == (Budgets{}) {
		return DefaultBudgets()
	}
	return c.Budgets
}
func result(j *Journal) intake.Plan {
	p := j.Plan
	p.State, p.Phase, p.Reason = j.State, j.Phase, j.Reason
	return p
}

func (c Controller) report(ctx context.Context, j *Journal) (intake.Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state, err := c.GitHub.Status(ctx, j.Plan.DeploymentID)
	if err != nil {
		return intake.Plan{}, err
	}
	if state != j.State {
		if state != "queued" && state != "in_progress" && state != "" {
			j.State, j.Reason = "error", "deployment status changed externally"
		} else if err = c.GitHub.WriteStatus(ctx, j.Plan.DeploymentID, j.State, j.Reason); err != nil {
			return intake.Plan{}, err
		}
	}
	j.Reported = true
	if err = c.Store.Save(*j); err != nil {
		return intake.Plan{}, err
	}
	return result(j), nil
}

func (c Controller) finish(ctx context.Context, j *Journal, state, reason string) (intake.Plan, error) {
	j.State, j.Reason = state, reason
	if state == "success" {
		j.LastError = ""
	}
	if err := c.Store.Save(*j); err != nil {
		return intake.Plan{}, err
	}
	if state != "success" {
		diagCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		observed, diagErr := c.Executor.Diagnose(diagCtx, j.Plan)
		cancel()
		diagnostic := map[string]any{"deployment_id": j.Plan.DeploymentID, "plan": j.Plan, "state": state, "phase": j.Phase, "reason": reason, "last_error": j.LastError, "deadline": j.Deadline, "phase_deadline": j.PhaseDeadline, "expected": j.Snapshot}
		if diagErr == nil && json.Valid(observed) {
			diagnostic["observed"] = json.RawMessage(observed)
		} else {
			diagnostic["collection_error"] = "cluster diagnostics unavailable"
		}
		raw, _ := json.Marshal(diagnostic)
		if err := c.Store.SaveDiagnostics(j.Plan.DeploymentID, raw); err != nil {
			fmt.Fprintln(os.Stderr, "cannot save deployment diagnostics:", err)
		}
	}
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return c.report(reportCtx, j)
}

func (c Controller) observe(ctx context.Context, j *Journal) (intake.Plan, error) {
	for {
		if ctx.Err() != nil {
			return result(j), ctx.Err()
		}
		if !c.Now().Before(j.Deadline) || !c.Now().Before(j.PhaseDeadline) {
			return c.finish(ctx, j, "error", timeoutReason(j.Phase))
		}
		phaseCtx, cancel := context.WithDeadline(ctx, j.PhaseDeadline)
		statusCtx, statusCancel := context.WithTimeout(phaseCtx, 15*time.Second)
		state, err := c.GitHub.Status(statusCtx, j.Plan.DeploymentID)
		statusCancel()
		if err != nil {
			cancel()
			j.LastError = err.Error()
			if ctx.Err() != nil {
				return result(j), ctx.Err()
			}
			if !c.Now().Before(j.PhaseDeadline) {
				return c.finish(ctx, j, "error", timeoutReason(j.Phase))
			}
			if saveErr := c.Store.Save(*j); saveErr != nil {
				return intake.Plan{}, saveErr
			}
			return result(j), err
		}
		if state != "in_progress" {
			cancel()
			j.State, j.Reason, j.Reported = "error", "deployment status changed during observation", true
			if err = c.Store.Save(*j); err != nil {
				return intake.Plan{}, err
			}
			return result(j), nil
		}
		if j.Phase == "waiting" {
			err = c.Executor.Wait(phaseCtx, j.Plan, j.Snapshot, j.PhaseDeadline)
		} else {
			err = c.Executor.Smoke(phaseCtx, j.Plan, j.Snapshot)
		}
		cancel()
		if ctx.Err() != nil {
			return result(j), ctx.Err()
		}
		if err != nil {
			j.LastError = err.Error()
			if !c.Now().Before(j.PhaseDeadline) || !c.Now().Before(j.Deadline) {
				return c.finish(ctx, j, "error", timeoutReason(j.Phase))
			}
			var failed *Failure
			if errors.As(err, &failed) {
				return c.finish(ctx, j, "failure", failed.Reason)
			}
			if saveErr := c.Store.Save(*j); saveErr != nil {
				return intake.Plan{}, saveErr
			}
			return result(j), err
		}
		if !c.Now().Before(j.PhaseDeadline) || !c.Now().Before(j.Deadline) {
			return c.finish(ctx, j, "error", timeoutReason(j.Phase))
		}
		if j.Phase == "smoke" {
			return c.finish(ctx, j, "success", "")
		}
		j.Phase = "smoke"
		j.LastError = ""
		j.PhaseDeadline = minTime(j.Deadline, c.Now().Add(c.budgets().Smoke))
		if err = c.Store.Save(*j); err != nil {
			return intake.Plan{}, err
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (c Controller) Step(ctx context.Context) (intake.Plan, error) {
	budgets := c.budgets()
	if err := budgets.Validate(); err != nil {
		return intake.Plan{}, err
	}
	j, err := c.Store.Load()
	if err != nil {
		return intake.Plan{}, err
	}
	if j != nil && !j.Reported {
		// A restart never repeats an apply whose result was not durably recorded.
		if j.State == "in_progress" {
			if j.Phase == "waiting" || j.Phase == "smoke" {
				return c.observe(ctx, j)
			}
			return c.finish(ctx, j, "error", "apply interrupted; outcome unknown")
		}
		return c.report(ctx, j)
	}
	deadline := c.Now().Add(budgets.Total)
	workCtx, workCancel := context.WithDeadline(ctx, deadline)
	defer workCancel()
	verifyCtx, cancel := context.WithTimeout(workCtx, 3*time.Minute)
	p, err := intake.Poll(verifyCtx, c.GitHub, c.Now)
	cancel()
	if err != nil || p.State != "validated" {
		return p, err
	}
	if j != nil && j.Plan.DeploymentID == p.DeploymentID {
		return c.report(ctx, j)
	}
	if j != nil && p.DeploymentID < j.Plan.DeploymentID {
		return intake.Plan{State: "idle"}, nil
	}
	if err = c.Executor.Ready(workCtx); err != nil {
		return intake.Plan{}, err
	}
	if err = c.Executor.Prepare(workCtx, p); err != nil {
		return intake.Plan{}, err
	}
	latest, err := c.GitHub.Latest(workCtx)
	if err != nil {
		return intake.Plan{}, err
	}
	if latest == nil || latest.ID != p.DeploymentID {
		return intake.Plan{}, fmt.Errorf("request superseded before apply")
	}
	state, err := c.GitHub.Status(workCtx, p.DeploymentID)
	if err != nil {
		return intake.Plan{}, err
	}
	if state != "queued" || p.ExpiresAt == nil || !c.Now().Before(*p.ExpiresAt) || !c.Now().Before(deadline) {
		return intake.Plan{}, fmt.Errorf("request is no longer queued or expired")
	}
	j = &Journal{Plan: p, State: "in_progress", Phase: "applying", Deadline: deadline}
	if err = c.Store.Save(*j); err != nil {
		return intake.Plan{}, err
	}
	if err = c.GitHub.WriteStatus(workCtx, p.DeploymentID, "in_progress"); err != nil {
		return intake.Plan{}, err
	}
	applyCtx, applyCancel := context.WithDeadline(workCtx, minTime(deadline, c.Now().Add(budgets.Apply)))
	snapshot, applyErr := c.Executor.Apply(applyCtx, p)
	applyCancel()
	if applyErr != nil {
		return c.finish(ctx, j, "error", "apply failed; outcome unknown")
	}
	if err = validateSnapshot(p, snapshot); err != nil {
		return c.finish(ctx, j, "error", "apply identity was not confirmed")
	}
	j.Snapshot, j.Phase = snapshot, "waiting"
	j.PhaseDeadline = minTime(deadline, c.Now().Add(budgets.Rollout))
	if err = c.Store.Save(*j); err != nil {
		return intake.Plan{}, err
	}
	return c.observe(ctx, j)
}

func timeoutReason(phase string) string {
	if phase == "waiting" {
		return "rollout timeout"
	}
	return phase + " timeout"
}
