package advisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const (
	DefaultBaseURL = "https://api.openai.com/v1"
	DefaultModel   = "gpt-4o-mini"
	maxBodyBytes   = 1 << 20
)

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

type ExplainInput struct {
	Pair           string   `json:"pair"`
	Diagnosis      string   `json:"rule_diagnosis"`
	Recommendation string   `json:"rule_recommendation"`
	RouteKind      string   `json:"route_kind"`
	RouteScore     int      `json:"route_score"`
	Fallback       string   `json:"fallback,omitempty"`
	NATClass       string   `json:"nat_class"`
	VPNDetected    bool     `json:"vpn_detected"`
	STUNAvailable  bool     `json:"stun_available"`
	TURNAvailable  bool     `json:"turn_available"`
	Warnings       []string `json:"warnings,omitempty"`
	Actions        []string `json:"rule_actions,omitempty"`
}

func Explain(ctx context.Context, config Config, input ExplainInput) (string, error) {
	endpoint, err := chatCompletionsURL(config.BaseURL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return "", errors.New("AI API key is not configured")
	}
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = DefaultModel
	}
	facts, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode AI explanation facts: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"model":       model,
		"temperature": 0.2,
		"max_tokens":  300,
		"messages": []map[string]string{
			{
				"role": "system",
				"content": "You explain Kigo file-transfer connectivity to a technical user. " +
					"Use plain language and no Markdown. Keep under 120 words. " +
					"Do not mention or request pairing codes, IP addresses, service URLs, interface names, file paths, or secrets. " +
					"Do not change the rule-selected route or recommendation. Explain the result and the listed next actions.",
			},
			{
				"role":    "user",
				"content": "Sanitized connectivity assessment JSON:\n" + string(facts),
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("encode AI explanation request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create AI explanation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	baseClient := config.Client
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	client := *baseClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("AI endpoint redirects are not allowed")
	}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("AI explanation request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
		return "", fmt.Errorf("AI explanation endpoint returned %s", res.Status)
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, maxBodyBytes))
	if err := decoder.Decode(&response); err != nil {
		return "", fmt.Errorf("decode AI explanation response: %w", err)
	}
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return "", errors.New("AI explanation response was empty")
	}
	return strings.TrimSpace(response.Choices[0].Message.Content), nil
}

func chatCompletionsURL(base string) (string, error) {
	if strings.TrimSpace(base) == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", errors.New("invalid AI base URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return "", errors.New("AI base URL must use HTTPS; HTTP is allowed only for loopback")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("AI base URL must not contain credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/chat/completions") {
		u.Path += "/chat/completions"
	}
	return u.String(), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
