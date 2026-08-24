package hook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	mux := Mux(func(string) {}, Secrets{GitHub: "s", Forgejo: "s"}, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestEmptySecretUnauthorized(t *testing.T) {
	mux := Mux(func(string) {}, Secrets{GitHub: "", Forgejo: "x"}, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/hook/github", bytes.NewReader([]byte("{}"))))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestGitHubHMAC(t *testing.T) {
	secret := "sekrit"
	body := []byte(`{"repository":{"full_name":"alice/demo"},"after":"abc"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	var got string
	mux := Mux(func(k string) { got = k }, Secrets{GitHub: secret, Forgejo: secret}, nil)
	req := httptest.NewRequest(http.MethodPost, "/hook/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "push")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if got != "alice/demo" {
		t.Fatalf("enq %q", got)
	}
}

func TestBadHMAC(t *testing.T) {
	mux := Mux(func(string) {}, Secrets{GitHub: "sekrit", Forgejo: "sekrit"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/hook/github", bytes.NewReader([]byte("{}")))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", rec.Code)
	}
}
