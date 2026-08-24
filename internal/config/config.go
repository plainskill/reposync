package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen         string        `yaml:"listen"`
	StateRoot      string        `yaml:"state_root"`
	SQLite         string        `yaml:"sqlite"`
	ReconcileEvery time.Duration `yaml:"reconcile_every"`
	GitTimeout     time.Duration `yaml:"git_timeout"`
	IncludeForks   bool          `yaml:"include_forks"`
	Bot            Bot           `yaml:"bot"`
	GitHub         GitHub        `yaml:"github"`
	Forgejo        Forgejo       `yaml:"forgejo"`
	Owners         []OwnerMap    `yaml:"owners"`
	Exclude        []string      `yaml:"exclude"`
}

type Bot struct {
	Name    string `yaml:"name"`
	Email   string `yaml:"email"`
	GitHub  string `yaml:"github"`
	Forgejo string `yaml:"forgejo"`
}

type GitHub struct {
	Token         string `yaml:"token"`
	API           string `yaml:"api"`
	Git           string `yaml:"git"`
	WebhookSecret string `yaml:"webhook_secret"`
}

type Forgejo struct {
	API           string `yaml:"api"`
	Git           string `yaml:"git"`
	Token         string `yaml:"token"`
	WebhookSecret string `yaml:"webhook_secret"`
}

type OwnerMap struct {
	GitHub  string `yaml:"github"`
	Forgejo string `yaml:"forgejo"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	expanded := os.ExpandEnv(string(raw))
	var c Config
	if err := yaml.Unmarshal([]byte(expanded), &c); err != nil {
		return nil, err
	}
	c.setDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) setDefaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:7745"
	}
	if c.ReconcileEvery == 0 {
		c.ReconcileEvery = 5 * time.Minute
	}
	if c.GitTimeout <= 0 {
		c.GitTimeout = 60 * time.Second
	}
	if c.Bot.Name == "" {
		c.Bot.Name = "reposync"
	}
	if c.Bot.Email == "" {
		c.Bot.Email = "reposync@localhost"
	}
	if c.Bot.Forgejo == "" {
		c.Bot.Forgejo = c.Bot.Name
	}
	if c.GitHub.API == "" {
		c.GitHub.API = "https://api.github.com"
	}
	if c.GitHub.Git == "" {
		c.GitHub.Git = "git@github.com:"
	}
	if c.Forgejo.API != "" {
		c.Forgejo.API = strings.TrimRight(c.Forgejo.API, "/")
	}
	if c.Forgejo.Git != "" {
		c.Forgejo.Git = strings.TrimRight(c.Forgejo.Git, "/")
	}
}

func (c *Config) validate() error {
	if c.StateRoot == "" {
		return fmt.Errorf("state_root is required")
	}
	if c.SQLite == "" {
		return fmt.Errorf("sqlite is required")
	}
	if c.GitHub.Token == "" {
		return fmt.Errorf("github.token is required")
	}
	if c.GitHub.WebhookSecret == "" {
		return fmt.Errorf("github.webhook_secret is required")
	}
	if c.Forgejo.API == "" {
		return fmt.Errorf("forgejo.api is required")
	}
	if c.Forgejo.Git == "" {
		return fmt.Errorf("forgejo.git is required")
	}
	if c.Forgejo.Token == "" {
		return fmt.Errorf("forgejo.token is required")
	}
	if c.Forgejo.WebhookSecret == "" {
		return fmt.Errorf("forgejo.webhook_secret is required")
	}
	if len(c.Owners) == 0 {
		return fmt.Errorf("at least one owners entry is required")
	}
	seenGH := map[string]struct{}{}
	seenFJ := map[string]struct{}{}
	for _, o := range c.Owners {
		if o.GitHub == "" || o.Forgejo == "" {
			return fmt.Errorf("owners entries need github and forgejo")
		}
		g := strings.ToLower(o.GitHub)
		f := strings.ToLower(o.Forgejo)
		if _, ok := seenGH[g]; ok {
			return fmt.Errorf("duplicate github owner %q", o.GitHub)
		}
		if _, ok := seenFJ[f]; ok {
			return fmt.Errorf("duplicate forgejo owner %q", o.Forgejo)
		}
		seenGH[g] = struct{}{}
		seenFJ[f] = struct{}{}
	}
	return nil
}

func (c *Config) IsBot(pusher string) bool {
	p := strings.TrimSpace(strings.ToLower(pusher))
	if p == "" {
		return false
	}
	for _, id := range []string{c.Bot.Name, c.Bot.GitHub, c.Bot.Forgejo} {
		if id != "" && strings.ToLower(id) == p {
			return true
		}
	}
	return false
}

func (c *Config) Excluded(fullName string) bool {
	n := strings.ToLower(strings.TrimSpace(fullName))
	for _, e := range c.Exclude {
		if strings.ToLower(strings.TrimSpace(e)) == n {
			return true
		}
	}
	return false
}

func (c *Config) ForgejoOwner(githubOwner string) (string, bool) {
	want := strings.ToLower(githubOwner)
	for _, o := range c.Owners {
		if strings.ToLower(o.GitHub) == want {
			return o.Forgejo, true
		}
	}
	return "", false
}

func (c *Config) GitHubOwner(forgejoOwner string) (string, bool) {
	want := strings.ToLower(forgejoOwner)
	for _, o := range c.Owners {
		if strings.ToLower(o.Forgejo) == want {
			return o.GitHub, true
		}
	}
	return "", false
}

func (c *Config) KnownGitHubOwner(owner string) bool {
	_, ok := c.ForgejoOwner(owner)
	return ok
}

func (c *Config) KnownForgejoOwner(owner string) bool {
	_, ok := c.GitHubOwner(owner)
	return ok
}

func (c *Config) GitHubGitURL(owner, name string) string {
	p := c.GitHub.Git
	if strings.Contains(p, "://") {
		return strings.TrimRight(p, "/") + "/" + owner + "/" + name + ".git"
	}
	return strings.TrimRight(p, ":") + ":" + owner + "/" + name + ".git"
}

func (c *Config) ForgejoGitURL(owner, name string) string {
	return c.Forgejo.Git + "/" + owner + "/" + name + ".git"
}

func (c *Config) HubsDir() string {
	return filepath.Join(c.StateRoot, "hubs")
}
