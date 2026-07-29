package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/suir1/kigo/internal/note"
	"github.com/suir1/kigo/internal/secure"
)

const localWebTokenHeader = "X-Kigo-Token"
const localWebMaxBodyBytes = 1 << 20

type localWebServer struct {
	token      string
	options    *globalOptions
	job        *nativeTaskStore
	note       *localWebNoteStore
	activityMu sync.Mutex
}

type localWebSendRequest struct {
	Path        string `json:"path"`
	Code        string `json:"code"`
	Symlinks    string `json:"symlinks"`
	NoGitIgnore bool   `json:"no_gitignore"`
}

type localWebRecvRequest struct {
	Code       string `json:"code"`
	OutputDir  string `json:"output_dir"`
	OnConflict string `json:"on_conflict"`
}

type localWebDoctorRequest struct {
	Timeout string `json:"timeout"`
}

type localWebBrowseEntry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Directory bool      `json:"directory"`
	Modified  time.Time `json:"modified"`
}

type localWebBrowseResponse struct {
	Current string                `json:"current"`
	Parent  string                `json:"parent,omitempty"`
	Mode    string                `json:"mode"`
	Sort    string                `json:"sort"`
	Entries []localWebBrowseEntry `json:"entries"`
}

type localWebNoteJoinRequest struct {
	Code string `json:"code"`
	Pad  string `json:"pad"`
}

type localWebNoteUpdateRequest struct {
	Text string `json:"text"`
}

type localWebNoteRecentRequest struct {
	Code     string `json:"code"`
	Pad      string `json:"pad"`
	Favorite bool   `json:"favorite"`
}

func newLocalWebCommand(g *globalOptions) *cobra.Command {
	var listen string
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Open a loopback-only browser console for native transfers",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := withInterrupt(cmd.Context())
			return runLocalWebConsole(ctx, g, listen, !noOpen)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "loopback address for the local console")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "print the local URL without opening a browser")
	return cmd
}

func runLocalWebConsole(ctx context.Context, g *globalOptions, listen string, open bool) error {
	if !localWebListenIsLoopback(listen) {
		return errors.New("local web console must listen on a loopback address")
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()

	token, err := generateLocalWebToken()
	if err != nil {
		return err
	}
	local := &localWebServer{
		token:   token,
		options: g,
		job:     &nativeTaskStore{run: newNativeTaskRunner(g)},
		note:    newLocalWebNoteStore(ctx, g),
	}
	server := &http.Server{
		Handler:           local.handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	url := "http://" + listener.Addr().String() + "/#token=" + token
	fmt.Println("Local web:", url)
	if open {
		if err := openLocalWebBrowser(url); err != nil {
			log.Printf("could not open browser: %v", err)
		}
	}
	go func() {
		<-ctx.Done()
		local.job.Cancel()
		local.note.Leave()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func localWebListenIsLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func generateLocalWebToken() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func (s *localWebServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/local.css", s.handleCSS)
	mux.HandleFunc("/local.js", s.handleJS)
	mux.HandleFunc("/api/config", s.auth(s.handleConfig))
	mux.HandleFunc("/api/job", s.auth(s.handleJob))
	mux.HandleFunc("/api/send", s.auth(s.handleSend))
	mux.HandleFunc("/api/recv", s.auth(s.handleRecv))
	mux.HandleFunc("/api/browse", s.auth(s.handleBrowse))
	mux.HandleFunc("/api/doctor", s.auth(s.handleDoctor))
	mux.HandleFunc("/api/job/cancel", s.auth(s.handleCancel))
	mux.HandleFunc("/api/note", s.auth(s.handleNote))
	mux.HandleFunc("/api/note/host", s.auth(s.handleNoteHost))
	mux.HandleFunc("/api/note/join", s.auth(s.handleNoteJoin))
	mux.HandleFunc("/api/note/update", s.auth(s.handleNoteUpdate))
	mux.HandleFunc("/api/note/clear", s.auth(s.handleNoteClear))
	mux.HandleFunc("/api/note/leave", s.auth(s.handleNoteLeave))
	mux.HandleFunc("/api/note/recents", s.auth(s.handleNoteRecents))
	mux.HandleFunc("/api/note/recents/favorite", s.auth(s.handleNoteRecentFavorite))
	mux.HandleFunc("/api/note/recents/forget", s.auth(s.handleNoteRecentForget))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *localWebServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		value := r.Header.Get(localWebTokenHeader)
		if subtle.ConstantTimeCompare([]byte(value), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *localWebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, localWebHTML)
	}
}

func (s *localWebServer) handleCSS(w http.ResponseWriter, r *http.Request) {
	serveLocalWebAsset(w, r, "text/css; charset=utf-8", localWebCSS)
}

func (s *localWebServer) handleJS(w http.ResponseWriter, r *http.Request) {
	serveLocalWebAsset(w, r, "text/javascript; charset=utf-8", localWebJS)
}

func serveLocalWebAsset(w http.ResponseWriter, r *http.Request, contentType, body string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, body)
	}
}

func (s *localWebServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	options := s.options
	if options == nil {
		options = &globalOptions{}
	}
	writeLocalWebJSON(w, http.StatusOK, map[string]any{
		"signal":       options.Signal,
		"web_url":      options.WebURL,
		"relay":        options.Relay,
		"proxy":        strings.TrimSpace(options.Proxy) != "",
		"interface":    options.Interface,
		"pair_timeout": normalizedPairTimeout(options).String(),
		"local":        options.Local,
		"no_direct":    nativeDirectDisabled(options),
	})
}

func (s *localWebServer) handleJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeLocalWebJSON(w, http.StatusOK, s.job.Snapshot())
}

func (s *localWebServer) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	path := r.URL.Query().Get("path")
	if len(path) > 4096 {
		writeLocalWebError(w, http.StatusBadRequest, "browse path is too long")
		return
	}
	modeName := strings.TrimSpace(r.URL.Query().Get("mode"))
	var mode pathPickMode
	switch modeName {
	case "", "send":
		modeName = "send"
		mode = pathPickFileOrDirectory
	case "directory":
		mode = pathPickDirectoryOnly
	default:
		writeLocalWebError(w, http.StatusBadRequest, "browse mode must be send or directory")
		return
	}
	sortName := strings.TrimSpace(r.URL.Query().Get("sort"))
	var sortMode pathBrowserSort
	switch sortName {
	case "", "name":
		sortName = "name"
		sortMode = pathBrowserSortName
	case "modified":
		sortMode = pathBrowserSortModified
	default:
		writeLocalWebError(w, http.StatusBadRequest, "browse sort must be name or modified")
		return
	}

	current, err := normalizeBrowserDirectory(path)
	if err != nil {
		writeLocalWebError(w, http.StatusBadRequest, err.Error())
		return
	}
	listed, err := listPathBrowserEntries(current, mode, sortMode)
	if err != nil {
		writeLocalWebError(w, http.StatusBadRequest, err.Error())
		return
	}
	response := localWebBrowseResponse{
		Current: current,
		Mode:    modeName,
		Sort:    sortName,
		Entries: make([]localWebBrowseEntry, 0, len(listed)),
	}
	for _, entry := range listed {
		if entry.Parent {
			response.Parent = entry.Path
			continue
		}
		if entry.SelectCurrent {
			continue
		}
		response.Entries = append(response.Entries, localWebBrowseEntry{
			Name:      strings.TrimSuffix(entry.Label, string(os.PathSeparator)),
			Path:      entry.Path,
			Directory: entry.IsDir,
			Modified:  entry.Modified,
		})
	}
	writeLocalWebJSON(w, http.StatusOK, response)
}

func (s *localWebServer) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var request localWebSendRequest
	if err := decodeLocalWebJSON(w, r, &request); err != nil {
		return
	}
	request.Path = strings.TrimSpace(request.Path)
	if request.Path == "" {
		writeLocalWebError(w, http.StatusBadRequest, "send path is required")
		return
	}
	if request.Symlinks == "" {
		request.Symlinks = "follow"
	}
	if request.Symlinks != "follow" && request.Symlinks != "preserve" {
		writeLocalWebError(w, http.StatusBadRequest, "symlinks must be follow or preserve")
		return
	}
	if request.Code != "" {
		code, err := secure.ValidateCode(request.Code)
		if err != nil {
			writeLocalWebError(w, http.StatusBadRequest, err.Error())
			return
		}
		request.Code = code
	}
	s.startJob(w, sendTaskRequest{
		Path:        request.Path,
		Code:        request.Code,
		Symlinks:    request.Symlinks,
		NoGitIgnore: request.NoGitIgnore,
		NoQRCode:    true,
	})
}

func (s *localWebServer) handleRecv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var request localWebRecvRequest
	if err := decodeLocalWebJSON(w, r, &request); err != nil {
		return
	}
	code, err := secure.ValidateCode(request.Code)
	if err != nil {
		writeLocalWebError(w, http.StatusBadRequest, err.Error())
		return
	}
	request.Code = code
	request.OutputDir = strings.TrimSpace(request.OutputDir)
	if request.OutputDir == "" {
		request.OutputDir = "."
	}
	if request.OnConflict == "" {
		request.OnConflict = "overwrite"
	}
	switch request.OnConflict {
	case "overwrite", "skip", "rename":
	default:
		writeLocalWebError(w, http.StatusBadRequest, "on_conflict must be overwrite, skip, or rename")
		return
	}
	s.startJob(w, receiveTaskRequest{
		Code:       request.Code,
		OutputDir:  request.OutputDir,
		OnConflict: request.OnConflict,
	})
}

func (s *localWebServer) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var request localWebDoctorRequest
	if err := decodeLocalWebJSON(w, r, &request); err != nil {
		return
	}
	if request.Timeout == "" {
		request.Timeout = "3s"
	}
	timeout, err := time.ParseDuration(request.Timeout)
	if err != nil {
		writeLocalWebError(w, http.StatusBadRequest, "invalid doctor timeout")
		return
	}
	s.startJob(w, doctorTaskRequest{Timeout: timeout, JSON: true})
}

func (s *localWebServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	s.job.Cancel()
	writeLocalWebJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *localWebServer) handleNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if s.note == nil {
		writeLocalWebError(w, http.StatusServiceUnavailable, "local notepad is unavailable")
		return
	}
	writeLocalWebJSON(w, http.StatusOK, s.note.Snapshot())
}

func (s *localWebServer) handleNoteHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var request localWebNoteJoinRequest
	if err := decodeLocalWebJSON(w, r, &request); err != nil {
		return
	}
	if request.Code != "" {
		code, err := secure.ValidateCode(request.Code)
		if err != nil {
			writeLocalWebError(w, http.StatusBadRequest, err.Error())
			return
		}
		request.Code = code
	}
	request.Pad = note.NormalizePad(request.Pad)
	if err := note.ValidatePad(request.Pad); err != nil {
		writeLocalWebError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.job != nil && s.job.Snapshot().Running {
		writeLocalWebError(w, http.StatusConflict, "a native task is already running")
		return
	}
	if s.note == nil {
		writeLocalWebError(w, http.StatusServiceUnavailable, "local notepad is unavailable")
		return
	}
	snapshot, err := s.note.StartHostWithCodeAndPad(request.Code, request.Pad)
	if err != nil {
		writeLocalWebError(w, http.StatusConflict, err.Error())
		return
	}
	writeLocalWebJSON(w, http.StatusAccepted, snapshot)
}

func (s *localWebServer) handleNoteJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var request localWebNoteJoinRequest
	if err := decodeLocalWebJSON(w, r, &request); err != nil {
		return
	}
	code, err := secure.ValidateCode(request.Code)
	if err != nil {
		writeLocalWebError(w, http.StatusBadRequest, err.Error())
		return
	}
	request.Pad = note.NormalizePad(request.Pad)
	if err := note.ValidatePad(request.Pad); err != nil {
		writeLocalWebError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.job != nil && s.job.Snapshot().Running {
		writeLocalWebError(w, http.StatusConflict, "a native task is already running")
		return
	}
	if s.note == nil {
		writeLocalWebError(w, http.StatusServiceUnavailable, "local notepad is unavailable")
		return
	}
	snapshot, err := s.note.StartJoinPad(code, request.Pad)
	if err != nil {
		writeLocalWebError(w, http.StatusConflict, err.Error())
		return
	}
	writeLocalWebJSON(w, http.StatusAccepted, snapshot)
}

func (s *localWebServer) handleNoteUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var request localWebNoteUpdateRequest
	if err := decodeLocalWebJSON(w, r, &request); err != nil {
		return
	}
	if err := note.ValidateText(request.Text); err != nil {
		writeLocalWebError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.note == nil {
		writeLocalWebError(w, http.StatusServiceUnavailable, "local notepad is unavailable")
		return
	}
	snapshot, err := s.note.Update(request.Text)
	if err != nil {
		writeLocalWebError(w, http.StatusConflict, err.Error())
		return
	}
	writeLocalWebJSON(w, http.StatusOK, snapshot)
}

func (s *localWebServer) handleNoteClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.note == nil {
		writeLocalWebError(w, http.StatusServiceUnavailable, "local notepad is unavailable")
		return
	}
	snapshot, err := s.note.Clear()
	if err != nil {
		writeLocalWebError(w, http.StatusConflict, err.Error())
		return
	}
	writeLocalWebJSON(w, http.StatusOK, snapshot)
}

func (s *localWebServer) handleNoteLeave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.note == nil {
		writeLocalWebError(w, http.StatusServiceUnavailable, "local notepad is unavailable")
		return
	}
	writeLocalWebJSON(w, http.StatusOK, s.note.Leave())
}

func (s *localWebServer) handleNoteRecents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if s.note == nil {
		writeLocalWebError(w, http.StatusServiceUnavailable, "local notepad is unavailable")
		return
	}
	entries, err := s.note.RecentNotes()
	if err != nil {
		writeLocalWebError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeLocalWebJSON(w, http.StatusOK, entries)
}

func (s *localWebServer) handleNoteRecentFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	request, ok := s.decodeNoteRecentRequest(w, r)
	if !ok {
		return
	}
	if err := s.note.SetRecentFavorite(request.Code, request.Pad, request.Favorite); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, note.ErrRecentNotFound) {
			status = http.StatusNotFound
		}
		writeLocalWebError(w, status, err.Error())
		return
	}
	writeLocalWebJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *localWebServer) handleNoteRecentForget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	request, ok := s.decodeNoteRecentRequest(w, r)
	if !ok {
		return
	}
	if err := s.note.ForgetRecent(request.Code, request.Pad); err != nil {
		writeLocalWebError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeLocalWebJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *localWebServer) decodeNoteRecentRequest(
	w http.ResponseWriter,
	r *http.Request,
) (localWebNoteRecentRequest, bool) {
	if s.note == nil {
		writeLocalWebError(w, http.StatusServiceUnavailable, "local notepad is unavailable")
		return localWebNoteRecentRequest{}, false
	}
	var request localWebNoteRecentRequest
	if err := decodeLocalWebJSON(w, r, &request); err != nil {
		return localWebNoteRecentRequest{}, false
	}
	code, err := secure.ValidateCode(request.Code)
	if err != nil {
		writeLocalWebError(w, http.StatusBadRequest, err.Error())
		return localWebNoteRecentRequest{}, false
	}
	request.Code = code
	request.Pad = note.NormalizePad(request.Pad)
	if err := note.ValidatePad(request.Pad); err != nil {
		writeLocalWebError(w, http.StatusBadRequest, err.Error())
		return localWebNoteRecentRequest{}, false
	}
	return request, true
}

func (s *localWebServer) startJob(w http.ResponseWriter, request nativeTaskRequest) {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.note != nil && s.note.Snapshot().Running {
		writeLocalWebError(w, http.StatusConflict, "a notepad session is already running")
		return
	}
	if err := s.job.Start(context.Background(), request); err != nil {
		writeLocalWebError(w, http.StatusConflict, err.Error())
		return
	}
	writeLocalWebJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func decodeLocalWebJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, localWebMaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeLocalWebError(w, http.StatusBadRequest, "invalid JSON body")
		return err
	}
	return nil
}

func writeLocalWebJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeLocalWebError(w http.ResponseWriter, status int, message string) {
	writeLocalWebJSON(w, status, map[string]string{"error": message})
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func openLocalWebBrowser(url string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
		args = []string{url}
	case "windows":
		command = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		command = "xdg-open"
		args = []string{url}
	}
	return exec.Command(command, args...).Start()
}
