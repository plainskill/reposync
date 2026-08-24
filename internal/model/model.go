package model

import (
	"encoding/json"
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

type metaJSON struct {
	Description   string   `json:"description"`
	Homepage      string   `json:"homepage"`
	DefaultBranch string   `json:"default_branch"`
	Private       bool     `json:"private"`
	Archived      bool     `json:"archived"`
	Topics        []string `json:"topics"`
}

func (m Meta) Snapshot() Meta {
	m.Topics = NormalizeTopics(m.Topics)
	m.UpdatedAt = time.Time{}
	return m
}

func EncodeMeta(m Meta) (string, error) {
	m = m.Snapshot()
	b, err := json.Marshal(metaJSON{
		Description: m.Description, Homepage: m.Homepage, DefaultBranch: m.DefaultBranch,
		Private: m.Private, Archived: m.Archived, Topics: m.Topics,
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func DecodeMeta(s string) (Meta, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Meta{}, false
	}
	var j metaJSON
	if err := json.Unmarshal([]byte(s), &j); err != nil {
		return Meta{}, false
	}
	return Meta{
		Description: j.Description, Homepage: j.Homepage, DefaultBranch: j.DefaultBranch,
		Private: j.Private, Archived: j.Archived, Topics: NormalizeTopics(j.Topics),
	}, true
}

// ReconcileMeta copies each field that changed vs the last snapshot.
// Description, homepage, default branch, private, archived, and topics are
// all treated the same way. Independent fields can move both directions in
// one pass. If the same field changed on both sides, UpdatedAt last-write-wins.
func ReconcileMeta(snap Meta, hasSnap bool, gh, fj Meta) (apply Meta, writeGH, writeFJ bool) {
	ghS, fjS := gh.Snapshot(), fj.Snapshot()
	if !hasSnap {
		if ghS.Equal(fjS) {
			return ghS, false, false
		}
		w, _ := Winner(gh, fj)
		apply = w.Snapshot()
		return apply, !apply.Equal(ghS), !apply.Equal(fjS)
	}
	snapS := snap.Snapshot()
	_, newer := Winner(gh, fj)
	pickStr := func(ghV, fjV, snapV string) string {
		return pick(ghV != snapV, fjV != snapV, ghV, fjV, snapV, newer)
	}
	pickBool := func(ghV, fjV, snapV bool) bool {
		return pick(ghV != snapV, fjV != snapV, ghV, fjV, snapV, newer)
	}
	apply = Meta{
		Description:   pickStr(ghS.Description, fjS.Description, snapS.Description),
		Homepage:      pickStr(ghS.Homepage, fjS.Homepage, snapS.Homepage),
		DefaultBranch: pickStr(ghS.DefaultBranch, fjS.DefaultBranch, snapS.DefaultBranch),
		Private:       pickBool(ghS.Private, fjS.Private, snapS.Private),
		Archived:      pickBool(ghS.Archived, fjS.Archived, snapS.Archived),
		Topics:        pick(!topicsEqual(ghS.Topics, snapS.Topics), !topicsEqual(fjS.Topics, snapS.Topics), ghS.Topics, fjS.Topics, snapS.Topics, newer),
	}
	apply.Topics = NormalizeTopics(apply.Topics)
	return apply, !apply.Equal(ghS), !apply.Equal(fjS)
}

func pick[T any](ghCh, fjCh bool, ghV, fjV, snapV T, newer string) T {
	switch {
	case ghCh && !fjCh:
		return ghV
	case fjCh && !ghCh:
		return fjV
	case ghCh && fjCh:
		if newer == "forgejo" {
			return fjV
		}
		return ghV
	default:
		return snapV
	}
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
