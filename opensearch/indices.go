package opensearch

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/oh-tarnished/opensearch-go-sdk/opensearchapi"
)

// Names names one or more aliases or templates for a path parameter.
func Names(names ...string) opensearchapi.CommonNames {
	if len(names) == 1 {
		return union[opensearchapi.CommonNames](strconv.Quote(names[0]))
	}
	data, _ := json.Marshal(names)
	return union[opensearchapi.CommonNames](string(data))
}

// CreateIndex creates index with body (settings, mappings, aliases), which
// may be nil.
func (c *Client) CreateIndex(ctx context.Context, index string, body any) error {
	r, err := jsonBody(body)
	if err != nil {
		return err
	}
	resp, err := c.Raw.IndicesCreateWithBody(ctx, index, nil, contentTypeJSON, r)
	return decode(resp, err, nil)
}

// IndexExists reports whether every named index, alias or pattern exists.
func (c *Client) IndexExists(ctx context.Context, indices ...string) (bool, error) {
	return exists(c.Raw.IndicesExists(ctx, Indices(indices...), nil))
}

// DeleteIndex deletes the named indices.
func (c *Client) DeleteIndex(ctx context.Context, indices ...string) error {
	resp, err := c.Raw.IndicesDelete(ctx, Indices(indices...), nil)
	return decode(resp, err, nil)
}

// PutMapping adds fields to the mapping of the named indices.
func (c *Client) PutMapping(ctx context.Context, mapping any, indices ...string) error {
	r, err := jsonBody(mapping)
	if err != nil {
		return err
	}
	resp, err := c.Raw.IndicesPutMappingWithBody(ctx, Indices(indices...), nil, contentTypeJSON, r)
	return decode(resp, err, nil)
}

// GetMapping returns the mappings of the named indices, keyed by concrete
// index name, each as raw JSON ({"mappings": {...}}).
func (c *Client) GetMapping(ctx context.Context, indices ...string) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	resp, err := c.Raw.IndicesGetMappingWithIndex(ctx, Indices(indices...), nil)
	return out, decode(resp, err, &out)
}

// Refresh makes recent writes to the named indices searchable.
func (c *Client) Refresh(ctx context.Context, indices ...string) error {
	resp, err := c.Raw.IndicesRefreshWithIndex(ctx, Indices(indices...), nil)
	return decode(resp, err, nil)
}

// AliasAction is one action of UpdateAliases. Set exactly one field.
type AliasAction struct {
	Add         *AliasSpec `json:"add,omitempty"`
	Remove      *AliasSpec `json:"remove,omitempty"`
	RemoveIndex *AliasSpec `json:"remove_index,omitempty"`
}

// AliasSpec names an index and an alias; Filter and Routing are optional.
type AliasSpec struct {
	Index        string   `json:"index,omitempty"`
	Indices      []string `json:"indices,omitempty"`
	Alias        string   `json:"alias,omitempty"`
	Aliases      []string `json:"aliases,omitempty"`
	Filter       any      `json:"filter,omitempty"`
	Routing      string   `json:"routing,omitempty"`
	IsWriteIndex *bool    `json:"is_write_index,omitempty"`
	MustExist    *bool    `json:"must_exist,omitempty"`
}

// UpdateAliases applies alias actions atomically.
func (c *Client) UpdateAliases(ctx context.Context, actions ...AliasAction) error {
	r, err := jsonBody(map[string]any{"actions": actions})
	if err != nil {
		return err
	}
	resp, err := c.Raw.IndicesUpdateAliasesWithBody(ctx, nil, contentTypeJSON, r)
	return decode(resp, err, nil)
}

// AliasIndices returns the concrete indices the alias points to; an alias
// that doesn't exist returns no indices and no error.
func (c *Client) AliasIndices(ctx context.Context, alias string) ([]string, error) {
	var out map[string]json.RawMessage
	resp, err := c.Raw.IndicesGetAliasWithName(ctx, Names(alias), nil)
	if err := decode(resp, err, &out); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	indices := make([]string, 0, len(out))
	for index := range out {
		indices = append(indices, index)
	}
	return indices, nil
}

// GetAlias returns the aliases of the named indices, keyed by index, each
// as raw JSON ({"aliases": {...}}).
func (c *Client) GetAlias(ctx context.Context, indices ...string) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	resp, err := c.Raw.IndicesGetAliasWithIndex(ctx, Indices(indices...), nil)
	return out, decode(resp, err, &out)
}

// PutIndexTemplate creates or replaces a composable index template.
func (c *Client) PutIndexTemplate(ctx context.Context, name string, template any) error {
	r, err := jsonBody(template)
	if err != nil {
		return err
	}
	resp, err := c.Raw.IndicesPutIndexTemplateWithBody(ctx, name, nil, contentTypeJSON, r)
	return decode(resp, err, nil)
}

// IndexTemplateExists reports whether the composable index template exists.
func (c *Client) IndexTemplateExists(ctx context.Context, name string) (bool, error) {
	return exists(c.Raw.IndicesExistsIndexTemplate(ctx, name, nil))
}

// DeleteIndexTemplate deletes a composable index template.
func (c *Client) DeleteIndexTemplate(ctx context.Context, name string) error {
	resp, err := c.Raw.IndicesDeleteIndexTemplate(ctx, name, nil)
	return decode(resp, err, nil)
}
