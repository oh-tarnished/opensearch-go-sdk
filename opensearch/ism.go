package opensearch

import (
	"context"
	"encoding/json"
)

// Index State Management (the ISM plugin). Policies are any JSON
// ({"policy": {...}}); responses are returned as raw JSON because they
// carry the stored policy back.

// PutISMPolicy creates the ISM policy id. To update an existing policy,
// use Client.Raw.ISMPutPolicyWithBody with if_seq_no and if_primary_term.
func (c *Client) PutISMPolicy(ctx context.Context, id string, policy any) (json.RawMessage, error) {
	r, err := jsonBody(policy)
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	resp, err := c.Raw.ISMPutPolicyWithBody(ctx, id, nil, contentTypeJSON, r)
	return out, decode(resp, err, &out)
}

// GetISMPolicy returns the ISM policy id.
func (c *Client) GetISMPolicy(ctx context.Context, id string) (json.RawMessage, error) {
	var out json.RawMessage
	resp, err := c.Raw.ISMGetPolicy(ctx, id, nil)
	return out, decode(resp, err, &out)
}

// DeleteISMPolicy deletes the ISM policy id.
func (c *Client) DeleteISMPolicy(ctx context.Context, id string) error {
	resp, err := c.Raw.ISMDeletePolicy(ctx, id, nil)
	return decode(resp, err, nil)
}

// AddISMPolicy attaches the ISM policy to index (a name or pattern).
func (c *Client) AddISMPolicy(ctx context.Context, index, policyID string) error {
	r, err := jsonBody(map[string]string{"policy_id": policyID})
	if err != nil {
		return err
	}
	var out struct {
		Failures bool              `json:"failures"`
		Failed   []json.RawMessage `json:"failed_indices"`
	}
	resp, err := c.Raw.ISMAddPolicyWithIndexWithBody(ctx, index, nil, contentTypeJSON, r)
	if err := decode(resp, err, &out); err != nil {
		return err
	}
	if out.Failures {
		reason := "policy not added"
		if len(out.Failed) > 0 {
			reason = string(out.Failed[0])
		}
		return &Error{StatusCode: 200, Type: "ism_add_policy_failed", Reason: reason}
	}
	return nil
}
