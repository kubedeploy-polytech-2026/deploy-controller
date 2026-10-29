package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"kubedeploy/deploy-controller/internal/intake"
)

type Identity struct {
	UID        string `json:"uid"`
	Generation int64  `json:"generation"`
	Image      string `json:"image"`
}
type Snapshot map[string]Identity

type Failure struct{ Reason string }

func (e *Failure) Error() string { return e.Reason }

func validatePlan(p intake.Plan) error {
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(p.EnvironmentRevision) {
		return fmt.Errorf("invalid environment revision")
	}
	for component, image := range map[string]string{"backend": p.BackendImage, "frontend": p.FrontendImage} {
		pattern := `^ghcr\.io/` + regexp.QuoteMeta(intake.Organization+"/"+component) + `@sha256:[a-f0-9]{64}$`
		if !regexp.MustCompile(pattern).MatchString(image) {
			return fmt.Errorf("invalid %s image", component)
		}
	}
	return nil
}

func validateSnapshot(p intake.Plan, snapshot Snapshot) error {
	if err := validatePlan(p); err != nil {
		return err
	}
	if len(snapshot) != 2 {
		return fmt.Errorf("expected two deployment identities")
	}
	for name, image := range map[string]string{"backend": p.BackendImage, "frontend": p.FrontendImage} {
		identity := snapshot[name]
		if identity.UID == "" || identity.Generation <= 0 || identity.Image != image {
			return fmt.Errorf("invalid %s deployment identity", name)
		}
	}
	return nil
}

func (c *Commands) snapshot(ctx context.Context, p intake.Plan) (Snapshot, bool, bool, error) {
	raw, err := c.Run(ctx, "", nil, "kubectl", "--context", "k3d-kubedeploy", "--namespace", "kubedeploy-demo", "--request-timeout=10s", "get", "deployments", "backend", "frontend", "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return nil, false, false, err
	}
	var response struct {
		Items []struct {
			Metadata struct {
				Name       string `json:"name"`
				UID        string `json:"uid"`
				Generation int64  `json:"generation"`
			} `json:"metadata"`
			Spec struct {
				Replicas int `json:"replicas"`
				Template struct {
					Spec struct {
						Containers []struct {
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				ObservedGeneration int64 `json:"observedGeneration"`
				Replicas           int   `json:"replicas"`
				Ready              int   `json:"readyReplicas"`
				Updated            int   `json:"updatedReplicas"`
				Available          int   `json:"availableReplicas"`
				Conditions         []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
					Reason string `json:"reason"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return nil, false, false, fmt.Errorf("invalid deployment response")
	}
	identities := make(Snapshot)
	ready, stalled := true, false
	if len(response.Items) != 2 {
		return nil, false, false, &Failure{Reason: "application deployment is missing"}
	}
	for _, d := range response.Items {
		if len(d.Spec.Template.Spec.Containers) != 1 {
			return nil, false, false, &Failure{Reason: "unexpected application containers"}
		}
		if _, exists := identities[d.Metadata.Name]; exists {
			return nil, false, false, fmt.Errorf("duplicate deployment identity")
		}
		identities[d.Metadata.Name] = Identity{UID: d.Metadata.UID, Generation: d.Metadata.Generation, Image: d.Spec.Template.Spec.Containers[0].Image}
		observed := d.Status.ObservedGeneration >= d.Metadata.Generation
		ready = ready && observed && d.Spec.Replicas > 0 && d.Status.Replicas == d.Spec.Replicas && d.Status.Ready == d.Spec.Replicas && d.Status.Updated == d.Spec.Replicas && d.Status.Available == d.Spec.Replicas
		for _, condition := range d.Status.Conditions {
			if observed && condition.Type == "Progressing" && condition.Status == "False" && condition.Reason == "ProgressDeadlineExceeded" {
				stalled = true
			}
		}
	}
	if err = validateSnapshot(p, identities); err != nil {
		return nil, false, false, &Failure{Reason: "application images or identities do not match the plan"}
	}
	return identities, ready, stalled, nil
}

func (c *Commands) check(ctx context.Context, p intake.Plan, expected Snapshot) (bool, error) {
	if err := validateSnapshot(p, expected); err != nil {
		return false, err
	}
	actual, ready, stalled, err := c.snapshot(ctx, p)
	if err != nil {
		return false, err
	}
	for name, identity := range expected {
		if actual[name] != identity {
			return false, &Failure{Reason: "deployment changed during observation"}
		}
	}
	if stalled && !ready {
		return false, &Failure{Reason: "rollout stopped progressing"}
	}
	return ready, nil
}
