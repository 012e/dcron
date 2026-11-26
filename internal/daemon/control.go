package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrDaemonNotRunning indicates the daemon control endpoint is unavailable.
var ErrDaemonNotRunning = errors.New("dcron daemon is not running")

// TriggerRequest asks the daemon to manually execute a specific job.
type TriggerRequest struct {
	RepoPath string `json:"repo_path"`
	JobName  string `json:"job_name"`
	Wait     bool   `json:"wait,omitempty"`
}

// TriggerResponse reports the outcome of a manual trigger request.
type TriggerResponse struct {
	RepoName   string `json:"repo_name,omitempty"`
	JobName    string `json:"job_name,omitempty"`
	Completed  bool   `json:"completed,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Controller communicates with a running dcron daemon over its local control endpoint.
type Controller interface {
	Trigger(ctx context.Context, req TriggerRequest) (TriggerResponse, error)
}

type controlInfo struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

// DefaultControlPath returns the cross-platform control descriptor path inside stateDir.
func DefaultControlPath(stateDir string) string {
	return filepath.Join(stateDir, "control.json")
}

// HTTPControlClient sends authenticated control commands to the running daemon over loopback HTTP.
// Using 127.0.0.1 + a mode-0600 bearer token file works identically across Linux, macOS, and Windows.
type HTTPControlClient struct {
	controlPath string
	httpClient  *http.Client
}

// NewHTTPControlClient constructs an HTTPControlClient reading connection metadata from controlPath.
func NewHTTPControlClient(controlPath string) *HTTPControlClient {
	return &HTTPControlClient{
		controlPath: controlPath,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			},
		},
	}
}

// Trigger sends a manual trigger request to the running daemon.
func (c *HTTPControlClient) Trigger(ctx context.Context, req TriggerRequest) (TriggerResponse, error) {
	info, err := readControlInfo(c.controlPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TriggerResponse{}, fmt.Errorf("%w (%s not found)", ErrDaemonNotRunning, c.controlPath)
		}
		return TriggerResponse{}, fmt.Errorf("read daemon control file: %w", err)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return TriggerResponse{}, fmt.Errorf("encode trigger request: %w", err)
	}

	url := fmt.Sprintf("http://%s/trigger", info.Addr)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return TriggerResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+info.Token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if isDaemonUnavailable(err) {
			return TriggerResponse{}, fmt.Errorf("%w (%s): %v", ErrDaemonNotRunning, info.Addr, err)
		}
		return TriggerResponse{}, fmt.Errorf("contact daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out TriggerResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return TriggerResponse{}, fmt.Errorf("decode daemon response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 {
		msg := out.Error
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return out, errors.New(msg)
	}
	return out, nil
}

func isDaemonUnavailable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "dial"
	}
	return false
}

func readControlInfo(path string) (controlInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return controlInfo{}, err
	}
	var info controlInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return controlInfo{}, err
	}
	if info.Addr == "" || info.Token == "" {
		return controlInfo{}, fmt.Errorf("%w: incomplete control file", ErrDaemonNotRunning)
	}
	return info, nil
}

func writeControlInfo(path string, info controlInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	if err := renameWithRetry(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func renameWithRetry(src, dst string) error {
	var err error
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = 5
	}
	for i := range attempts {
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if i+1 < attempts {
			time.Sleep(time.Duration(i+1) * 15 * time.Millisecond)
		}
	}
	return err
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type controlServer struct {
	controlPath string
	listener    net.Listener
	server      *http.Server
}

func startControlServer(controlPath string, svc *Service) (*controlServer, error) {
	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("generate control token: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}

	info := controlInfo{
		Addr:  ln.Addr().String(),
		Token: token,
		PID:   svc.pid,
	}
	if err := writeControlInfo(controlPath, info); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("write control file %s: %w", controlPath, err)
	}

	expectedAuth := "Bearer " + token
	authorize := func(r *http.Request) bool {
		got := r.Header.Get("Authorization")
		return len(got) == len(expectedAuth) && subtle.ConstantTimeCompare([]byte(got), []byte(expectedAuth)) == 1
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(r) {
			writeJSON(w, http.StatusUnauthorized, TriggerResponse{Error: "unauthorized"})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/trigger", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(r) {
			writeJSON(w, http.StatusUnauthorized, TriggerResponse{Error: "unauthorized"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, TriggerResponse{Error: "method not allowed"})
			return
		}
		var req TriggerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, TriggerResponse{Error: fmt.Sprintf("invalid request: %v", err)})
			return
		}
		resp, err := svc.TriggerJob(r.Context(), req)
		if err != nil {
			if resp.Error == "" {
				resp.Error = err.Error()
			}
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "already running") {
				status = http.StatusConflict
			}
			writeJSON(w, status, resp)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			svc.logger.Warn("control server stopped with error", "err", err)
		}
	}()

	return &controlServer{
		controlPath: controlPath,
		listener:    ln,
		server:      srv,
	}, nil
}

func (s *controlServer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
	_ = s.listener.Close()
	_ = os.Remove(s.controlPath)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
