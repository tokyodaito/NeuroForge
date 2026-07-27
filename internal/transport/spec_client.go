package transport

import (
	"context"
	"strconv"
)

// ---- Spec client methods ----
//
// These mirror the daemon-mediated spec endpoints under /tasks/{id}/specification*.
// They are the wire path the CLI uses (the offline `forge spec compile` in
// internal/cli/spec_cmd.go bypasses these — it calls task.Compile directly).

// CompileSpec calls POST /tasks/{id}/specification/compile. The id argument is
// placed in the URL path; req.TaskID is overwritten with id so callers cannot
// accidentally address a different task through the body.
func (c *Client) CompileSpec(ctx context.Context, id string, req CompileSpecRequest) (CompileSpecResultDTO, error) {
	req.TaskID = id
	var out CompileSpecResultDTO
	if err := c.postJSON(ctx, "/tasks/"+id+"/specification/compile", req, &out); err != nil {
		return CompileSpecResultDTO{}, err
	}
	return out, nil
}

// GetSpecification calls GET /tasks/{id}/specification?version=N. version <= 0
// returns the latest version.
func (c *Client) GetSpecification(ctx context.Context, id string, version int) (SpecificationDTO, error) {
	path := "/tasks/" + id + "/specification"
	if version > 0 {
		path += "?version=" + strconv.Itoa(version)
	}
	var out SpecificationDTO
	if err := c.getJSON(ctx, path, &out); err != nil {
		return SpecificationDTO{}, err
	}
	return out, nil
}

// ListSpecificationVersions calls GET /tasks/{id}/specification/versions.
func (c *Client) ListSpecificationVersions(ctx context.Context, id string) ([]int, error) {
	var out []int
	if err := c.getJSON(ctx, "/tasks/"+id+"/specification/versions", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LockSpecification calls POST /tasks/{id}/specification/lock. The error is
// returned unwrapped (matching RemoveProject / ReopenTask) so callers can use
// errors.As/APIError type-assertions on the HTTP status code.
func (c *Client) LockSpecification(ctx context.Context, id string, req LockSpecRequest) (SpecificationDTO, error) {
	req.TaskID = id
	var out SpecificationDTO
	if err := c.postJSON(ctx, "/tasks/"+id+"/specification/lock", req, &out); err != nil {
		return SpecificationDTO{}, err
	}
	return out, nil
}
