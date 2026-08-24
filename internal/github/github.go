package github

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
	if api == "" {
		api = "https://api.github.com"
	}
	return &Client{API: strings.TrimRight(api, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type repoJSON struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Homepage      string     `json:"homepage"`
	DefaultBranch string     `json:"default_branch"`
	Private       bool       `json:"private"`
	Archived      bool       `json:"archived"`
	Fork          bool       `json:"fork"`
	Topics        []string   `json:"topics"`
	Size          int64      `json:"size"`
	PushedAt      *time.Time `json:"pushed_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Owner         struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
}

func (r repoJSON) listed() model.Listed {
	return model.Listed{
		ID:     r.ID,
		Owner:  r.Owner.Login,
		Name:   r.Name,
		Fork:   r.Fork,
		Org:    strings.EqualFold(r.Owner.Type, "Organization"),
		Mirror: false,
		Empty:  r.Size == 0 && r.PushedAt == nil,
		Meta: model.Meta{
			Description:   r.Description,
			Homepage:      r.Homepage,
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
	if kind == "Organization" {
		return nil, fmt.Errorf("github owner %q is an organization; reposync only syncs username/* user repos", owner)
	}
	q := url.Values{"per_page": {"100"}, "type": {"owner"}}
	path := "/users/" + url.PathEscape(owner) + "/repos"
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
			if item.Org || !strings.EqualFold(item.Owner, owner) {
				continue
			}
			if len(item.Meta.Topics) == 0 {
				topics, err := c.Topics(ctx, r.Owner.Login, r.Name)
				if err == nil {
					item.Meta.Topics = topics
				}
			}
			all = append(all, item)
		}
		if len(raw) < 100 {
			break
		}
		page++
	}
	return all, nil
}

func (c *Client) Get(ctx context.Context, owner, name string) (*model.Listed, error) {
	var raw repoJSON
	err := c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, &raw)
	if err != nil {
		return nil, err
	}
	item := raw.listed()
	topics, tErr := c.Topics(ctx, owner, name)
	if tErr == nil {
		item.Meta.Topics = topics
	}
	return &item, nil
}

func (c *Client) Create(ctx context.Context, owner string, name string, meta model.Meta) (*model.Listed, error) {
	kind, err := c.ownerType(ctx, owner)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"name":          name,
		"description":   meta.Description,
		"homepage":      meta.Homepage,
		"private":       meta.Private,
		"auto_init":     false,
		"has_issues":    false,
		"has_projects":  false,
		"has_wiki":      false,
		"has_downloads": false,
	}
	if kind == "Organization" {
		return nil, fmt.Errorf("github owner %q is an organization; reposync only creates username/* user repos", owner)
	}
	path := "/user/repos"
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
		"name":           name,
		"description":    meta.Description,
		"homepage":       meta.Homepage,
		"private":        meta.Private,
		"archived":       meta.Archived,
		"default_branch": meta.DefaultBranch,
	}
	if meta.DefaultBranch == "" {
		delete(body, "default_branch")
	}
	if err := c.do(ctx, http.MethodPatch, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), body, nil); err != nil {
		return err
	}
	return c.SetTopics(ctx, owner, name, meta.Topics)
}

func (c *Client) Rename(ctx context.Context, owner, name, newName string) error {
	return c.do(ctx, http.MethodPatch, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), map[string]any{"name": newName}, nil)
}

func (c *Client) Topics(ctx context.Context, owner, name string) ([]string, error) {
	var out struct {
		Names []string `json:"names"`
	}
	err := c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/topics", nil, &out)
	if err != nil {
		return nil, err
	}
	return model.NormalizeTopics(out.Names), nil
}

func (c *Client) SetTopics(ctx context.Context, owner, name string, topics []string) error {
	return c.do(ctx, http.MethodPut, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/topics",
		map[string]any{"names": model.NormalizeTopics(topics)}, nil)
}

func (c *Client) ownerType(ctx context.Context, owner string) (string, error) {
	var out struct {
		Type string `json:"type"`
	}
	if err := c.do(ctx, http.MethodGet, "/users/"+url.PathEscape(owner), nil, &out); err != nil {
		return "", err
	}
	return out.Type, nil
}

type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("github: HTTP %d: %s", e.Status, e.Body)
}

func IsNotFound(err error) bool {
	var he *HTTPError
	return err != nil && errorsAsHTTP(err, &he) && he.Status == 404
}

func errorsAsHTTP(err error, dest **HTTPError) bool {
	he, ok := err.(*HTTPError)
	if ok {
		*dest = he
	}
	return ok
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
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
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
