package advisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExplainSendsBoundedCompatibleRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "test-model" || len(body.Messages) != 2 {
			t.Fatalf("body = %#v", body)
		}
		if !strings.Contains(body.Messages[1].Content, `"rule_recommendation":"configure_turn"`) {
			t.Fatalf("user content = %q", body.Messages[1].Content)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": "  Configure TURN, then retry the browser route.  "},
			}},
		})
	}))
	defer server.Close()

	text, err := Explain(context.Background(), Config{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-key",
		Model:   "test-model",
	}, ExplainInput{
		Pair:           "web-web",
		Diagnosis:      "WebRTC is selected.",
		Recommendation: "configure_turn",
		RouteKind:      "webrtc",
		NATClass:       "symmetric",
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "Configure TURN, then retry the browser route." {
		t.Fatalf("text = %q", text)
	}
}

func TestExplainRejectsInsecureRemoteEndpoint(t *testing.T) {
	_, err := Explain(context.Background(), Config{
		BaseURL: "http://example.com/v1",
		APIKey:  "test-key",
	}, ExplainInput{})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("err = %v", err)
	}
}

func TestExplainRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	_, err := Explain(context.Background(), Config{BaseURL: server.URL, APIKey: "test-key"}, ExplainInput{})
	if err == nil || !strings.Contains(err.Error(), "redirects are not allowed") {
		t.Fatalf("err = %v", err)
	}
}

func TestExplainRejectsEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	_, err := Explain(context.Background(), Config{BaseURL: server.URL, APIKey: "test-key"}, ExplainInput{})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v", err)
	}
}
