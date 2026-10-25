package intake

import (
	"context"
	"fmt"
	"strconv"
)

func (g GitHub) WriteStatus(ctx context.Context, id int64, state string) error {
	descriptions := map[string]string{
		"in_progress": "Applying verified environment revision",
		"success":     "Rollouts and Gateway smoke passed",
		"failure":     "Deployment failed; inspect controller output",
		"error":       "Controller interrupted; inspect cluster before creating a new request",
	}
	description, ok := descriptions[state]
	if !ok || id <= 0 {
		return fmt.Errorf("invalid deployment status")
	}
	_, err := g.Run(ctx, "api", "--hostname", "github.com", "--method", "POST",
		"repos/"+Repository+"/deployments/"+strconv.FormatInt(id, 10)+"/statuses",
		"-f", "state="+state, "-f", "description="+description, "-F", "auto_inactive=false")
	return err
}
