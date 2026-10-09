package runtime

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"

	"infrapilot/internal/logs"
)

// maxLogLine caps one log message (SEC-1 T19): a longer line keeps its first
// maxLogLine bytes plus truncMarker, so a tenant cannot fill shared Redis.
const (
	maxLogLine  = 16 << 10
	truncMarker = " …[truncated]"
)

// sinkTimeout bounds one sink write, so a hung Redis cannot pin a stream
// (SEC-4 T19).
const sinkTimeout = 2 * time.Second

// LogSink receives log lines; *logs.Redis implements it.
type LogSink interface {
	Write(ctx context.Context, e logs.Entry) error
}

// Streamer follows `docker logs -f` for deployed containers and forwards each
// line to the sink. Streams live until the container exits, Stop is called or
// the root context ends.
type Streamer struct {
	ctx    context.Context
	sink   LogSink
	log    *slog.Logger
	now    func() time.Time
	newCmd func(ctx context.Context, containerID string) *exec.Cmd

	mu     sync.Mutex
	active map[string]*stream
}

// stream is one running `docker logs -f`; its pointer identifies it, so the
// cleanup of a stopped stream never removes a newer one (STAFF-2 T19).
type stream struct{ cancel context.CancelFunc }

func NewStreamer(ctx context.Context, sink LogSink, log *slog.Logger) *Streamer {
	return &Streamer{
		ctx: ctx, sink: sink, log: log, now: time.Now,
		newCmd: func(ctx context.Context, containerID string) *exec.Cmd {
			return exec.CommandContext(ctx, "docker", "logs", "-f", "--tail=100", containerID)
		},
		active: map[string]*stream{},
	}
}

// Start begins streaming unless this deployment is already streaming. An
// empty deployment ID or an invalid container name is logged and ignored.
func (s *Streamer) Start(deploymentID, containerID string) {
	if err := checkName("container", containerID); err != nil || deploymentID == "" {
		s.log.Warn("log stream not started", "deploymentId", deploymentID, "containerId", containerID, "err", err)
		return
	}
	s.mu.Lock()
	if _, ok := s.active[deploymentID]; ok {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	st := &stream{cancel: cancel}
	s.active[deploymentID] = st
	s.mu.Unlock()

	s.log.Info("log stream starting", "deploymentId", deploymentID, "containerId", containerID)
	go s.run(ctx, deploymentID, st, s.newCmd(ctx, containerID))
}

func (s *Streamer) Stop(deploymentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.active[deploymentID]; ok {
		st.cancel()
		delete(s.active, deploymentID)
	}
}

// release ends st and forgets it, unless a newer stream took its place.
func (s *Streamer) release(deploymentID string, st *stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.cancel()
	if s.active[deploymentID] == st {
		delete(s.active, deploymentID)
	}
}

func (s *Streamer) IsStreaming(deploymentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.active[deploymentID]
	return ok
}

func (s *Streamer) run(ctx context.Context, deploymentID string, st *stream, cmd *exec.Cmd) {
	defer s.release(deploymentID, st)
	emitCtx := context.WithoutCancel(ctx)
	cmd.WaitDelay = waitDelay

	stdout, err := cmd.StdoutPipe()
	if err == nil {
		var stderr io.ReadCloser
		if stderr, err = cmd.StderrPipe(); err == nil {
			if err = cmd.Start(); err == nil {
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); s.pump(emitCtx, deploymentID, "stdout", stdout) }()
				go func() { defer wg.Done(); s.pump(emitCtx, deploymentID, "stderr", stderr) }()
				wg.Wait()
				_ = cmd.Wait()
				code := cmd.ProcessState.ExitCode()
				s.emit(emitCtx, deploymentID, "system", fmt.Sprintf("Log stream ended (exit code: %d)", code))
				return
			}
		}
	}
	s.emit(emitCtx, deploymentID, "system", "Log stream error: "+err.Error())
}

// pump reads r line by line until EOF. A line longer than maxLogLine is cut
// to that size plus truncMarker and the rest of it is discarded, so memory
// stays bounded and the pipe is always drained (SEC-1, SEC-2 T19).
func (s *Streamer) pump(ctx context.Context, deploymentID, source string, r io.Reader) {
	br := bufio.NewReaderSize(r, maxLogLine)
	line := make([]byte, 0, maxLogLine)
	cut := false
	for {
		chunk, err := br.ReadSlice('\n')
		if err == nil {
			chunk = chunk[:len(chunk)-1]
		}
		if !cut {
			if room := maxLogLine - len(line); len(chunk) > room {
				line, cut = append(line, chunk[:room]...), true
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if msg := logLine(line, cut); msg != "" {
			s.emit(ctx, deploymentID, source, msg)
		}
		line, cut = line[:0], false
		if err != nil {
			if err != io.EOF {
				s.log.Warn("log stream read failed", "deploymentId", deploymentID, "source", source, "err", err)
			}
			return
		}
	}
}

// logLine turns a raw line into a message: a trailing \r is dropped and a cut
// line ends on a whole UTF-8 rune followed by truncMarker.
func logLine(line []byte, cut bool) string {
	if !cut {
		return string(bytes.TrimSuffix(line, []byte("\r")))
	}
	for i := len(line) - 1; i >= 0 && i >= len(line)-utf8.UTFMax; i-- {
		if utf8.RuneStart(line[i]) {
			if !utf8.FullRune(line[i:]) {
				line = line[:i]
			}
			break
		}
	}
	return string(line) + truncMarker
}

func (s *Streamer) emit(ctx context.Context, deploymentID, source, msg string) {
	e := logs.Entry{DeploymentID: deploymentID, Source: source, Message: msg, Timestamp: logs.FormatTime(s.now())}
	ctx, cancel := context.WithTimeout(ctx, sinkTimeout)
	defer cancel()
	if err := s.sink.Write(ctx, e); err != nil {
		s.log.Warn("log write failed", "deploymentId", deploymentID, "err", err)
	}
}
