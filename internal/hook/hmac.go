package hook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

type Enqueue func(pairKey string)

type Secrets struct {
	GitHub  string
	Forgejo string
}

func Mux(enq Enqueue, secrets Secrets, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/hook/github", func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, "github", secrets.GitHub, GitHubSignatureOK, enq, log)
	})
	mux.HandleFunc("/hook/forgejo", func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, "forgejo", secrets.Forgejo, ForgejoSignatureOK, enq, log)
	})
	return mux
}

func handle(w http.ResponseWriter, r *http.Request, source, secret string, check func(string, []byte, string) bool, enq Enqueue, log *slog.Logger) {
	if secret == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sig := r.Header.Get("X-Hub-Signature-256")
	if source == "forgejo" {
		if h := r.Header.Get("X-Forgejo-Signature"); h != "" {
			sig = h
		} else if h := r.Header.Get("X-Gitea-Signature"); h != "" {
			sig = h
		}
	}
	if !check(secret, body, sig) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if source == "forgejo" {
		if event == "" {
			event = r.Header.Get("X-Forgejo-Event")
		}
		if event == "" {
			event = r.Header.Get("X-Gitea-Event")
		}
	}
	if strings.EqualFold(event, "ping") {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	repo, sha, del := parseRepo(body)
	if del {
		enq("*")
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if repo == "" {
		enq("*")
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if log != nil {
		log.Info("hook", "source", source, "repo", repo, "sha", sha, "event", event)
	}
	enq(repo)
	w.WriteHeader(http.StatusAccepted)
}

func parseRepo(body []byte) (repo, sha string, deleted bool) {
	var p struct {
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		Action     string `json:"action"`
		Ref        string `json:"ref"`
		Repository struct {
			FullName string `json:"full_name"`
			Name     string `json:"name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", "", false
	}
	repo = p.Repository.FullName
	if repo == "" && p.Repository.Owner.Login != "" && p.Repository.Name != "" {
		repo = p.Repository.Owner.Login + "/" + p.Repository.Name
	}
	sha = strings.ToLower(strings.TrimSpace(p.After))
	deleted = p.Deleted || p.Action == "deleted" || sha == "0000000000000000000000000000000000000000"
	return repo, sha, deleted
}

func GitHubSignatureOK(secret string, body []byte, header string) bool {
	return hmacHeaderOK(secret, body, header, "sha256=")
}

func ForgejoSignatureOK(secret string, body []byte, header string) bool {
	if header == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(header), "sha256=") {
		return hmacHeaderOK(secret, body, header, "sha256=")
	}
	return hmacHexOK(secret, body, header)
}

func hmacHeaderOK(secret string, body []byte, header, prefix string) bool {
	if secret == "" || header == "" {
		return false
	}
	h := strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(h), strings.ToLower(prefix)) {
		return false
	}
	return hmacHexOK(secret, body, h[len(prefix):])
}

func hmacHexOK(secret string, body []byte, gotHex string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	got := strings.TrimSpace(strings.ToLower(gotHex))
	return hmac.Equal([]byte(want), []byte(got))
}
