package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalWebListenIsLoopback(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:0", "[::1]:8080", "localhost:9000"} {
		if !localWebListenIsLoopback(listen) {
			t.Fatalf("loopback listen was rejected: %s", listen)
		}
	}
	for _, listen := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "192.0.2.1:8080", "bad"} {
		if localWebListenIsLoopback(listen) {
			t.Fatalf("non-loopback listen was accepted: %s", listen)
		}
	}
}

func TestLocalWebAPIRequiresTokenAndReturnsSafeConfig(t *testing.T) {
	server := &localWebServer{
		token: "test-token",
		options: &globalOptions{
			Signal:      "https://signal.example",
			WebURL:      "https://kigo.example",
			Relay:       "relay.example:9000",
			RelayPass:   "relay-secret",
			Proxy:       "http://alice:secret@proxy.example:8080",
			Interface:   "utun3",
			PairTimeout: 2 * time.Minute,
			NoDirect:    true,
		},
		job: &nativeTaskStore{run: func(context.Context, nativeTaskRequest, func(nativeTaskEvent)) error { return nil }},
	}
	handler := server.handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/config", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	wrongMethod := localWebJSONRequest(t, handler, http.MethodPost, "/api/config", "")
	if wrongMethod.Code != http.StatusMethodNotAllowed || wrongMethod.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("wrong method status=%d allow=%q", wrongMethod.Code, wrongMethod.Header().Get("Allow"))
	}

	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.Header.Set(localWebTokenHeader, "test-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("config status = %d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Signal      string `json:"signal"`
		WebURL      string `json:"web_url"`
		Relay       string `json:"relay"`
		Proxy       bool   `json:"proxy"`
		PairTimeout string `json:"pair_timeout"`
		NoDirect    bool   `json:"no_direct"`
		Interface   string `json:"interface"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Signal != "https://signal.example" ||
		body.WebURL != "https://kigo.example" ||
		body.Relay != "relay.example:9000" ||
		!body.Proxy ||
		body.PairTimeout != "2m0s" ||
		!body.NoDirect ||
		body.Interface != "utun3" {
		t.Fatalf("config body = %#v", body)
	}
	if strings.Contains(response.Body.String(), "alice") || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("config exposed proxy credentials: %s", response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("local web response is missing CSP")
	}
}

func TestLocalWebBrowseListsSendPathsAndDirectoryOnlyMode(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "payload.txt")
	if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &localWebServer{
		token: "test-token",
		job:   &nativeTaskStore{run: func(context.Context, nativeTaskRequest, func(nativeTaskEvent)) error { return nil }},
	}
	handler := server.handler()

	browse := func(mode, sort string) (localWebBrowseResponse, *httptest.ResponseRecorder) {
		t.Helper()
		path := "/api/browse?path=" + url.QueryEscape(root) + "&mode=" + mode + "&sort=" + sort
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set(localWebTokenHeader, "test-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var body localWebBrowseResponse
		if response.Code == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		}
		return body, response
	}

	send, response := browse("send", "name")
	if response.Code != http.StatusOK {
		t.Fatalf("browse status = %d body=%s", response.Code, response.Body.String())
	}
	if send.Current != root || send.Mode != "send" || send.Sort != "name" || send.Parent == "" {
		t.Fatalf("browse response = %#v", send)
	}
	if len(send.Entries) != 2 || send.Entries[0].Name != "child" || !send.Entries[0].Directory ||
		send.Entries[1].Name != "payload.txt" || send.Entries[1].Directory {
		t.Fatalf("browse entries = %#v", send.Entries)
	}

	directories, response := browse("directory", "modified")
	if response.Code != http.StatusOK || len(directories.Entries) != 1 ||
		directories.Entries[0].Name != "child" || directories.Sort != "modified" {
		t.Fatalf("directory browse status=%d response=%#v", response.Code, directories)
	}

	_, response = browse("invalid", "name")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "browse mode") {
		t.Fatalf("invalid mode status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLocalWebSendJobCapturesCodeLinkAndRejectsOverlap(t *testing.T) {
	started := make(chan nativeTaskRequest, 1)
	release := make(chan struct{})
	job := &nativeTaskStore{
		run: func(ctx context.Context, request nativeTaskRequest, emit func(nativeTaskEvent)) error {
			started <- request
			emit(nativeTaskEvent{Kind: nativeTaskEventLine, Text: "Prepared: 1 file"})
			emit(nativeTaskEvent{Kind: nativeTaskEventCode, Text: "Code: K7M9Q2", Value: "K7M9Q2"})
			emit(nativeTaskEvent{Kind: nativeTaskEventLink, Text: "Link: https://kigo.example/#c=K7M9Q2", Value: "https://kigo.example/#c=K7M9Q2"})
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	server := &localWebServer{
		token:   "test-token",
		options: &globalOptions{Signal: "https://signal.example"},
		job:     job,
	}
	handler := server.handler()

	response := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/send",
		`{"path":"/tmp/file.txt","code":"release-2026","symlinks":"preserve","no_gitignore":true}`,
	)
	if response.Code != http.StatusAccepted {
		t.Fatalf("send status = %d body=%s", response.Code, response.Body.String())
	}
	request, ok := (<-started).(sendTaskRequest)
	if !ok {
		t.Fatalf("send request has unexpected type")
	}
	if request.Path != "/tmp/file.txt" || request.Symlinks != "preserve" ||
		!request.NoGitIgnore || request.Code != "RELEASE-2026" {
		t.Fatalf("send request = %#v", request)
	}

	overlap := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/doctor",
		`{"timeout":"2s"}`,
	)
	if overlap.Code != http.StatusConflict {
		t.Fatalf("overlap status = %d body=%s", overlap.Code, overlap.Body.String())
	}

	waitForLocalWebJob(t, job, func(state nativeTaskSnapshot) bool {
		return state.Code == "K7M9Q2" && state.Link != ""
	})
	close(release)
	state := waitForLocalWebJob(t, job, func(state nativeTaskSnapshot) bool { return !state.Running })
	if state.Failed || state.Canceled || state.Code != "K7M9Q2" {
		t.Fatalf("completed state = %#v", state)
	}
}

func TestLocalWebCancelPropagatesToJob(t *testing.T) {
	started := make(chan struct{})
	job := &nativeTaskStore{
		run: func(ctx context.Context, _ nativeTaskRequest, _ func(nativeTaskEvent)) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	server := &localWebServer{token: "test-token", job: job}
	handler := server.handler()

	response := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/recv",
		`{"code":"K7M9Q2","output_dir":".","on_conflict":"rename"}`,
	)
	if response.Code != http.StatusAccepted {
		t.Fatalf("recv status = %d body=%s", response.Code, response.Body.String())
	}
	<-started

	cancel := localWebJSONRequest(t, handler, http.MethodPost, "/api/job/cancel", `{}`)
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", cancel.Code, cancel.Body.String())
	}
	state := waitForLocalWebJob(t, job, func(state nativeTaskSnapshot) bool { return !state.Running })
	if !state.Canceled || state.Failed {
		t.Fatalf("canceled state = %#v", state)
	}
}

func TestLocalWebReceiveRejectsInvalidPairingCode(t *testing.T) {
	started := make(chan struct{}, 1)
	server := &localWebServer{
		token: "test-token",
		job: &nativeTaskStore{
			run: func(context.Context, nativeTaskRequest, func(nativeTaskEvent)) error {
				started <- struct{}{}
				return nil
			},
		},
	}
	response := localWebJSONRequest(
		t,
		server.handler(),
		http.MethodPost,
		"/api/recv",
		`{"code":"INVALID!","output_dir":".","on_conflict":"overwrite"}`,
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("recv status = %d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "pairing code") {
		t.Fatalf("recv body = %s", response.Body.String())
	}
	select {
	case <-started:
		t.Fatal("invalid receive request started a native task")
	default:
	}
}

func TestLocalWebSendRejectsInvalidCustomCode(t *testing.T) {
	started := false
	server := &localWebServer{
		token: "test-token",
		job: &nativeTaskStore{run: func(context.Context, nativeTaskRequest, func(nativeTaskEvent)) error {
			started = true
			return nil
		}},
	}
	response := localWebJSONRequest(
		t,
		server.handler(),
		http.MethodPost,
		"/api/send",
		`{"path":"/tmp/file.txt","code":"bad!","symlinks":"follow"}`,
	)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "pairing code") {
		t.Fatalf("response status=%d body=%s", response.Code, response.Body.String())
	}
	if started {
		t.Fatal("invalid custom code started a native task")
	}
}

func TestLocalWebJobUsesLastChildErrorLine(t *testing.T) {
	job := &nativeTaskStore{
		run: func(_ context.Context, _ nativeTaskRequest, emit func(nativeTaskEvent)) error {
			emit(nativeTaskEvent{Kind: nativeTaskEventLine, Text: "Waiting for receiver..."})
			emit(nativeTaskEvent{Kind: nativeTaskEventLine, Text: "error: pairing room expired"})
			return errors.New("exit status 1")
		},
	}
	if err := job.Start(context.Background(), sendTaskRequest{Path: "/tmp/file", Symlinks: "follow"}); err != nil {
		t.Fatal(err)
	}
	state := waitForLocalWebJob(t, job, func(state nativeTaskSnapshot) bool { return !state.Running })
	if !state.Failed || state.Error != "pairing room expired" {
		t.Fatalf("failed state = %#v", state)
	}
}

func localWebJSONRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set(localWebTokenHeader, "test-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func waitForLocalWebJob(
	t *testing.T,
	job *nativeTaskStore,
	condition func(nativeTaskSnapshot) bool,
) nativeTaskSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := job.Snapshot()
		if condition(state) {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	state := job.Snapshot()
	t.Fatalf("job condition not met: %#v", state)
	return nativeTaskSnapshot{}
}
