package execution

import (
	"context"
	"encoding/json"
	"fmt"

	"kubedeploy/deploy-controller/internal/intake"
)

func (c *Commands) Diagnose(ctx context.Context, _ intake.Plan) ([]byte, error) {
	raw, err := c.Run(ctx, "", nil, "kubectl", "--context", "k3d-kubedeploy", "--namespace", "kubedeploy-demo", "--request-timeout=10s", "get", "deployments,pods,events", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name       string `json:"name"`
				UID        string `json:"uid,omitempty"`
				Generation int64  `json:"generation,omitempty"`
			} `json:"metadata"`
			Reason  string `json:"reason,omitempty"`
			Message string `json:"message,omitempty"`
			Status  struct {
				Phase              string `json:"phase,omitempty"`
				ObservedGeneration int64  `json:"observedGeneration,omitempty"`
				Ready              int    `json:"readyReplicas,omitempty"`
				Conditions         []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
					Reason string `json:"reason,omitempty"`
				} `json:"conditions,omitempty"`
				Containers []struct {
					Name     string `json:"name"`
					Ready    bool   `json:"ready"`
					Restarts int    `json:"restartCount"`
					State    struct {
						Waiting *struct {
							Reason string `json:"reason"`
						} `json:"waiting,omitempty"`
						Terminated *struct {
							Reason   string `json:"reason"`
							ExitCode int    `json:"exitCode"`
						} `json:"terminated,omitempty"`
					} `json:"state"`
				} `json:"containerStatuses,omitempty"`
			} `json:"status"`
		} `json:"items"`
	}
	if err = json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("invalid diagnostics response")
	}
	if len(list.Items) > 100 {
		list.Items = list.Items[len(list.Items)-100:]
	}
	return json.Marshal(list)
}
