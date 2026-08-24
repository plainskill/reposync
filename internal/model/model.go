package model

import (
	"sort"
	"strings"
	"time"
)

type Listed struct {
	ID      int64
	Owner   string
	Name    string
	Fork    bool
	Org     bool
	Mirror  bool
	Empty   bool
	Missing bool
	Meta    Meta
}

type Meta struct {
	Description   string
	Homepage      string
	DefaultBranch string
	Private       bool
	Archived      bool
	Topics        []string
	UpdatedAt     time.Time
}

func (m Meta) Equal(o Meta) bool {
	return m.Description == o.Description &&
		m.Homepage == o.Homepage &&
		m.DefaultBranch == o.DefaultBranch &&
		m.Private == o.Private &&
		m.Archived == o.Archived &&
		topicsEqual(m.Topics, o.Topics)
}

func topicsEqual(a, b []string) bool {
	a = NormalizeTopics(a)
	b = NormalizeTopics(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func NormalizeTopics(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, t := range in {
		t = strings.TrimSpace(strings.ToLower(t))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Winner returns the newer metadata. Equal timestamps prefer github.
func Winner(github, forgejo Meta) (Meta, string) {
	if github.UpdatedAt.After(forgejo.UpdatedAt) {
		return github, "github"
	}
	if forgejo.UpdatedAt.After(github.UpdatedAt) {
		return forgejo, "forgejo"
	}
	return github, "github"
}

func ConflictRef(branch string) string {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	return "refs/heads/reposync/conflict/" + branch
}

func IsConflictRef(ref string) bool {
	return strings.HasPrefix(ref, "refs/heads/reposync/conflict/") ||
		strings.HasPrefix(ref, "reposync/conflict/")
}

func BranchName(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}
