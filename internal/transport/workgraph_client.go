package transport

import "context"

// ---- WorkGraph client methods ----
//
// Mirror of the daemon work-graph endpoint under /tasks/{id}/workgraph.

// GetWorkGraph calls GET /tasks/{id}/workgraph. Returns the task's work graph
// (packages, readiness verdicts, and the active leases in scope) as observed
// by the daemon at call time.
func (c *Client) GetWorkGraph(ctx context.Context, id string) (WorkGraphDTO, error) {
	var out WorkGraphDTO
	if err := c.getJSON(ctx, "/tasks/"+id+"/workgraph", &out); err != nil {
		return WorkGraphDTO{}, err
	}
	return out, nil
}
