package execution

import (
	"context"
	"fmt"
	"time"

	"kubedeploy/deploy-controller/internal/intake"
)

type GitHub interface {
	intake.Source
	WriteStatus(context.Context, int64, string) error
}

type Executor interface {
	Ready(context.Context) error
	Apply(context.Context, intake.Plan) error
}

type Controller struct {
	GitHub   GitHub
	Executor Executor
	Store    *Store
	Now      func() time.Time
}

func (c Controller) report(ctx context.Context, j *Journal) (intake.Plan, error) {
	if err := c.GitHub.WriteStatus(ctx, j.Plan.DeploymentID, j.State); err != nil {
		return intake.Plan{}, err
	}
	j.Reported = true
	if err := c.Store.Save(*j); err != nil {
		return intake.Plan{}, err
	}
	p := j.Plan
	p.State = j.State
	return p, nil
}

func (c Controller) Step(ctx context.Context) (intake.Plan, error) {
	j, err := c.Store.Load()
	if err != nil {
		return intake.Plan{}, err
	}
	if j != nil && !j.Reported {
		// A restart never repeats an apply whose result was not durably recorded.
		if j.State == "in_progress" {
			j.State = "error"
			if err = c.Store.Save(*j); err != nil {
				return intake.Plan{}, err
			}
		}
		return c.report(ctx, j)
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
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
	if err = c.Executor.Ready(ctx); err != nil {
		return intake.Plan{}, err
	}
	latest, err := c.GitHub.Latest(ctx)
	if err != nil {
		return intake.Plan{}, err
	}
	if latest == nil || latest.ID != p.DeploymentID {
		return intake.Plan{}, fmt.Errorf("request superseded before apply")
	}
	state, err := c.GitHub.Status(ctx, p.DeploymentID)
	if err != nil {
		return intake.Plan{}, err
	}
	if state != "queued" || p.ExpiresAt == nil || !c.Now().Before(*p.ExpiresAt) {
		return intake.Plan{}, fmt.Errorf("request is no longer queued or expired")
	}
	j = &Journal{Plan: p, State: "in_progress"}
	if err = c.Store.Save(*j); err != nil {
		return intake.Plan{}, err
	}
	if err = c.GitHub.WriteStatus(ctx, p.DeploymentID, "in_progress"); err != nil {
		return intake.Plan{}, err
	}
	applyCtx, applyCancel := context.WithTimeout(ctx, 10*time.Minute)
	applyErr := c.Executor.Apply(applyCtx, p)
	applyCancel()
	j.State = "success"
	if applyErr != nil {
		j.State = "failure"
	}
	if err = c.Store.Save(*j); err != nil {
		return intake.Plan{}, err
	}
	reportCtx, reportCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer reportCancel()
	result, err := c.report(reportCtx, j)
	if err != nil {
		return intake.Plan{}, err
	}
	return result, applyErr
}
