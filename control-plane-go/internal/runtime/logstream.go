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
	"sync/atomic"
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
func (s *Streamer) Start(deploymentID, containerID string) { s.start(deploymentID, containerID) }

// start is Start reporting whether a new stream was started (STAFF-1 T20).
// Start keeps no result so *Streamer still satisfies the plan's LogStarter.
func (s *Streamer) start(deploymentID, containerID string) bool {
	if err := checkName("container", containerID); err != nil || deploymentID == "" {
		s.log.Warn("log stream not started", "deploymentId", deploymentID, "containerId", containerID, "err", err)
		return false
	}
	s.mu.Lock()
	if _, ok := s.active[deploymentID]; ok {
		s.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(s.ctx)
	st := &stream{cancel: cancel}
	s.active[deploymentID] = st
	s.mu.Unlock()

	s.log.Info("log stream starting", "deploymentId", deploymentID, "containerId", containerID)
	go s.run(ctx, deploymentID, st, s.newCmd(ctx, containerID))
	return true
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
	w := &lineWriter{s: s, deploymentID: deploymentID}
	cmd.WaitDelay = waitDelay

	stdout, err := cmd.StdoutPipe()
	if err == nil {
		var stderr io.ReadCloser
		if stderr, err = cmd.StderrPipe(); err == nil {
			if err = cmd.Start(); err == nil {
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); s.pump(ctx, w, "stdout", stdout) }()
				go func() { defer wg.Done(); s.pump(ctx, w, "stderr", stderr) }()
				wg.Wait()
				_ = cmd.Wait()
				msg := "Log stream stopped"
				if ctx.Err() == nil {
					msg = fmt.Sprintf("Log stream ended (exit code: %d)", cmd.ProcessState.ExitCode())
				}
				s.end(ctx, w, msg)
				return
			}
		}
	}
	s.end(ctx, w, "Log stream error: "+err.Error())
}

// end writes the stream's final system entry, except on worker shutdown:
// then the container keeps running and the stream resumes on the next start,
// so telling the UI it ended would be false (STAFF-4(a), VER-4 T19).
// The entry is written even after Stop (WithoutCancel, bounded by
// sinkTimeout) and even after earlier lines failed.
func (s *Streamer) end(ctx context.Context, w *lineWriter, msg string) {
	if s.ctx.Err() != nil {
		s.log.Info("log stream closed for shutdown", "deploymentId", w.deploymentID)
		return
	}
	w.failed.Store(false)
	w.write(context.WithoutCancel(ctx), "system", msg)
}

// pump reads r line by line until EOF. A line longer than maxLogLine is cut
// to that size plus truncMarker and the rest of it is discarded, so memory
// stays bounded and the pipe is always drained (SEC-1, SEC-2 T19).
func (s *Streamer) pump(ctx context.Context, w *lineWriter, source string, r io.Reader) {
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
			w.write(ctx, source, msg)
		}
		line, cut = line[:0], false
		if err != nil {
			if err != io.EOF {
				s.log.Warn("log stream read failed", "deploymentId", w.deploymentID, "source", source, "err", err)
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

// lineWriter writes one stream's entries. After the first failed write it
// warns once and drops the stream's remaining lines, so a Redis outage costs
// one Warn and at most one sinkTimeout per stream; pump keeps draining the
// pipe either way (STAFF-5, SEC-6 T19).
type lineWriter struct {
	s            *Streamer
	deploymentID string
	failed       atomic.Bool
	warned       atomic.Bool
}

// write sends one entry. A write cut short because ctx ended (Stop or
// shutdown) is dropped silently: that is not a sink failure.
func (w *lineWriter) write(ctx context.Context, source, msg string) {
	if w.failed.Load() || ctx.Err() != nil {
		return
	}
	e := logs.Entry{DeploymentID: w.deploymentID, Source: source, Message: msg, Timestamp: logs.FormatTime(w.s.now())}
	wctx, cancel := context.WithTimeout(ctx, sinkTimeout)
	defer cancel()
	err := w.s.sink.Write(wctx, e)
	if err == nil || ctx.Err() != nil {
		return
	}
	w.failed.Store(true)
	if w.warned.CompareAndSwap(false, true) {
		w.s.log.Warn("log write failed; dropping the rest of this stream", "deploymentId", w.deploymentID, "err", err)
	}
}
