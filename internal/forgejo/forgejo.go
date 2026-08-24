package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"reposync/internal/model"
)

type Client struct {
	API   string
	Token string
	HTTP  *http.Client
}

func New(api, token string) *Client {
	return &Client{API: strings.TrimRight(api, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type repoJSON struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Website       string    `json:"website"`
	DefaultBranch string    `json:"default_branch"`
	Private       bool      `json:"private"`
	Archived      bool      `json:"archived"`
	Fork          bool      `json:"fork"`
	Mirror        bool      `json:"mirror"`
	Empty         bool      `json:"empty"`
	Topics        []string  `json:"topics"`
	UpdatedAt     time.Time `json:"updated_at"`
	Owner         struct {
		Login    string `json:"login"`
		UserName string `json:"username"`
	} `json:"owner"`
}

func (r repoJSON) listed() model.Listed {
	owner := r.Owner.Login
	if owner == "" {
		owner = r.Owner.UserName
	}
	return model.Listed{
		ID:     r.ID,
		Owner:  owner,
		Name:   r.Name,
		Fork:   r.Fork,
		Mirror: r.Mirror,
		Empty:  r.Empty,
		Meta: model.Meta{
			Description:   r.Description,
			Homepage:      r.Website,
			DefaultBranch: r.DefaultBranch,
			Private:       r.Private,
			Archived:      r.Archived,
			Topics:        model.NormalizeTopics(r.Topics),
			UpdatedAt:     r.UpdatedAt,
		},
	}
}

func (c *Client) List(ctx context.Context, owner string) ([]model.Listed, error) {
	kind, err := c.ownerType(ctx, owner)
	if err != nil {
		return nil, err
	}
	var path string
	q := url.Values{"limit": {"50"}}
	if kind == "Organization" {
		path = "/api/v1/orgs/" + url.PathEscape(owner) + "/repos"
	} else {
		path = "/api/v1/users/" + url.PathEscape(owner) + "/repos"
	}
	var all []model.Listed
	page := 1
	for {
		q.Set("page", strconv.Itoa(page))
		var raw []repoJSON
		if err := c.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &raw); err != nil {
			return nil, err
		}
		for _, r := range raw {
			item := r.listed()
			if len(item.Meta.Topics) == 0 {
				if topics, err := c.Topics(ctx, item.Owner, item.Name); err == nil {
					item.Meta.Topics = topics
				}
			}
			all = append(all, item)
		}
		if len(raw) < 50 {
			break
		}
		page++
	}
	return all, nil
}

func (c *Client) Get(ctx context.Context, owner, name string) (*model.Listed, error) {
	var raw repoJSON
	err := c.do(ctx, http.MethodGet, "/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, &raw)
	if err != nil {
		return nil, err
	}
	item := raw.listed()
	if topics, err := c.Topics(ctx, owner, name); err == nil {
		item.Meta.Topics = topics
	}
	return &item, nil
}

func (c *Client) Create(ctx context.Context, owner, name string, meta model.Meta) (*model.Listed, error) {
	kind, err := c.ownerType(ctx, owner)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"name":        name,
		"description": meta.Description,
		"private":     meta.Private,
		"auto_init":   false,
	}
	var path string
	if kind == "Organization" {
		path = "/api/v1/orgs/" + url.PathEscape(owner) + "/repos"
	} else {
		path = "/api/v1/user/repos"
	}
	var raw repoJSON
	if err := c.do(ctx, http.MethodPost, path, body, &raw); err != nil {
		return nil, err
	}
	item := raw.listed()
	if err := c.Update(ctx, item.Owner, item.Name, meta); err != nil {
		return &item, err
	}
	got, err := c.Get(ctx, item.Owner, item.Name)
	if err != nil {
		return &item, nil
	}
	return got, nil
}

func (c *Client) Update(ctx context.Context, owner, name string, meta model.Meta) error {
	body := map[string]any{
		"description":    meta.Description,
		"website":        meta.Homepage,
		"private":        meta.Private,
		"archived":       meta.Archived,
		"default_branch": meta.DefaultBranch,
	}
	if meta.DefaultBranch == "" {
		delete(body, "default_branch")
	}
	if err := c.do(ctx, http.MethodPatch, "/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), body, nil); err != nil {
		return err
	}
	return c.SetTopics(ctx, owner, name, meta.Topics)
}

func (c *Client) Rename(ctx context.Context, owner, name, newName string) error {
	return c.do(ctx, http.MethodPatch, "/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), map[string]any{"name": newName}, nil)
}

func (c *Client) Topics(ctx context.Context, owner, name string) ([]string, error) {
	var out struct {
		Topics []string `json:"topics"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/topics", nil, &out)
	if err != nil {
		return nil, err
	}
	return model.NormalizeTopics(out.Topics), nil
}

func (c *Client) SetTopics(ctx context.Context, owner, name string, topics []string) error {
	return c.do(ctx, http.MethodPut, "/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/topics",
		map[string]any{"topics": model.NormalizeTopics(topics)}, nil)
}

func (c *Client) ownerType(ctx context.Context, owner string) (string, error) {
	var org json.RawMessage
	err := c.do(ctx, http.MethodGet, "/api/v1/orgs/"+url.PathEscape(owner), nil, &org)
	if err == nil {
		return "Organization", nil
	}
	if !IsNotFound(err) {
		return "", err
	}
	return "User", nil
}

type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("forgejo: HTTP %d: %s", e.Status, e.Body)
}

func IsNotFound(err error) bool {
	he, ok := err.(*HTTPError)
	return ok && he.Status == 404
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.API+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return &HTTPError{Status: res.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if out == nil || res.StatusCode == 204 || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
