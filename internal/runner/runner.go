package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/012e/dcron/internal/config"
)

// Request describes a single job execution.
type Request struct {
	RepoName string
	RepoPath string
	Job      config.Job
	LogPath  string
	Stdout   io.Writer // Optional additional sink (e.g., os.Stdout for `dcron run`).
	Stderr   io.Writer // Optional additional sink (e.g., os.Stderr for `dcron run`).
}

// Result captures the outcome of running a job.
type Result struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
	ExitCode   int
	Err        error
	LogPath    string
}

// Runner executes a configured job.
type Runner interface {
	Run(ctx context.Context, req Request) Result
}

// ExecRunner executes jobs via a shell subprocess with process-group timeout cleanup
// and structured append-only per-job logging.
type ExecRunner struct{}

// NewExecRunner constructs a new ExecRunner.
func NewExecRunner() *ExecRunner {
	return &ExecRunner{}
}

var nonSlugChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// LogPathFor returns a deterministic, collision-free log file path for a repo job.
func LogPathFor(logDir, repoName, repoPath, jobName string) string {
	sum := sha256.Sum256([]byte(repoPath))
	shortHash := hex.EncodeToString(sum[:4])
	repoSlug := sanitizeSlug(repoName)
	if repoSlug == "" {
		repoSlug = "repo"
	}
	jobSlug := sanitizeSlug(jobName)
	if jobSlug == "" {
		jobSlug = "job"
	}
	return filepath.Join(logDir, fmt.Sprintf("%s-%s", repoSlug, shortHash), jobSlug+".log")
}

func sanitizeSlug(s string) string {
	return strings.Trim(nonSlugChars.ReplaceAllString(s, "_"), "._-")
}

// Run executes req.Job in its configured shell, working directory, environment, and timeout.
func (r *ExecRunner) Run(ctx context.Context, req Request) Result {
	started := time.Now().UTC()
	res := Result{
		StartedAt: started,
		LogPath:   req.LogPath,
	}

	var logFile *os.File
	if req.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(req.LogPath), 0o755); err != nil {
			res.FinishedAt = time.Now().UTC()
			res.Duration = res.FinishedAt.Sub(started)
			res.ExitCode = -1
			res.Err = fmt.Errorf("create log directory: %w", err)
			return res
		}
		f, err := os.OpenFile(req.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			res.FinishedAt = time.Now().UTC()
			res.Duration = res.FinishedAt.Sub(started)
			res.ExitCode = -1
			res.Err = fmt.Errorf("open log file: %w", err)
			return res
		}
		logFile = f
		defer func() { _ = logFile.Close() }()
		_, _ = fmt.Fprintf(logFile, "\n=== [%s] START repo=%s job=%s cmd=%q ===\n",
			started.Format(time.RFC3339), req.RepoName, req.Job.Name, req.Job.Command)
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if req.Job.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Job.Timeout)
		defer cancel()
	}

	cmd := buildShellCommand(runCtx, req.Job.Shell, req.Job.Command)
	cmd.Dir = req.Job.WorkDir
	cmd.Env = buildEnv(req)
	configureProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second

	cmd.Stdout = combineWriters(logFile, req.Stdout)
	cmd.Stderr = combineWriters(logFile, req.Stderr)

	err := cmd.Run()
	finished := time.Now().UTC()
	res.FinishedAt = finished
	res.Duration = finished.Sub(started)

	if err != nil {
		res.Err = err
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			res.Err = fmt.Errorf("job timed out after %s: %w", req.Job.Timeout, err)
		}
	}

	if logFile != nil {
		if res.Err != nil {
			_, _ = fmt.Fprintf(logFile, "=== [%s] END exit=%d duration=%s err=%q ===\n",
				finished.Format(time.RFC3339), res.ExitCode, res.Duration.Round(time.Millisecond), res.Err.Error())
		} else {
			_, _ = fmt.Fprintf(logFile, "=== [%s] END exit=0 duration=%s ===\n",
				finished.Format(time.RFC3339), res.Duration.Round(time.Millisecond))
		}
	}

	return res
}

func combineWriters(file *os.File, extra io.Writer) io.Writer {
	switch {
	case file != nil && extra != nil:
		return io.MultiWriter(file, extra)
	case file != nil:
		return file
	case extra != nil:
		return extra
	default:
		return io.Discard
	}
}

func buildEnv(req Request) []string {
	overrides := make(map[string]string, len(req.Job.Env)+3)
	overrides["DCRON_REPO"] = req.RepoName
	overrides["DCRON_REPO_PATH"] = req.RepoPath
	overrides["DCRON_JOB"] = req.Job.Name
	for k, v := range req.Job.Env {
		overrides[k] = v
	}
	return mergeEnv(os.Environ(), overrides, runtime.GOOS == "windows")
}

func mergeEnv(base []string, overrides map[string]string, caseInsensitive bool) []string {
	merged := make(map[string]string, len(base)+len(overrides))
	normToKey := make(map[string]string, len(base)+len(overrides))

	set := func(k, v string) {
		if k == "" {
			return
		}
		if caseInsensitive {
			nk := strings.ToUpper(k)
			if prev, exists := normToKey[nk]; exists && prev != k {
				delete(merged, prev)
			}
			normToKey[nk] = k
		}
		merged[k] = v
	}

	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			set(k, v)
		}
	}

	overrideKeys := make([]string, 0, len(overrides))
	for k := range overrides {
		overrideKeys = append(overrideKeys, k)
	}
	sort.Strings(overrideKeys)
	for _, k := range overrideKeys {
		set(k, overrides[k])
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+merged[k])
	}
	return out
}
