# Plan 1, Task 19: Container log streaming

## Implementation
- Engineer commit `d3af276` feat(go): stream container logs into redis (`internal/runtime/{logstream.go,logstream_test.go}`). Plan-verbatim; production code runs docker with an argv list (`sh -c` only in tests). Engineer flagged: Stop/Start race, no WaitDelay, `sc.Err()` dropped on >1 MiB lines.

## Round 0 reviews (d3af276)
- **Security: CHANGES REQUESTED (1 high, 1 medium, 3 low).**
  - SEC-1 T19 high: messages up to 1 MiB go to Redis uncut (MaxLen 10000 per deployment + PUBLISH fan-out) → a tenant can fill shared Redis (~10 GiB per deployment) and break logs/Socket.io for everyone. Closes backlog T11 SEC-4 here.
  - SEC-2 T19 medium: a >1 MiB line ends `Scan` with ErrTooLong; the pump stops reading, docker blocks, the stream hangs silently and stays "streaming" (tenant can hide its output).
  - SEC-3 T19 low: containerID unvalidated in argv (leading `-`).
  - SEC-4 T19 low: no `cmd.WaitDelay`; `Write` has no timeout (hung Redis pins the pump).
  - SEC-5 T19 low: control chars/ANSI stored raw.
- **Staff: CHANGES REQUESTED (1 medium, 4 low).** STAFF-1 = SEC-2. STAFF-2 low: Stop/Start race (old goroutine's deferred `Stop` kills the new stream; latent, no production Stop caller yet). STAFF-3 low: WaitDelay (consistency only). STAFF-4 low forward: shutdown writes "ended (exit code: -1)" for live containers, resume duplicates up to 100 lines, no Streamer `Wait()` before `rdb.Close()`. STAFF-5 low: Warn per line when Redis is down; Stop test only checks the map.
- **Verifier: PASS with VER-1 (important).** Real Docker + dev Redis: stdout/stderr lines with ms timestamps; `docker stop` → "Log stream ended (exit code: 0)", IsStreaming false in 2.15 s; `Stop()` → goroutines back to baseline in 500 ms, exit -1. VER-1 = SEC-2 reproduced: after a 2 MiB line only "before" arrived, nothing after (stdout or stderr), and IsStreaming stayed true even after `docker stop`. VER-2 minor: `--tail=100` counts Docker's 16 KiB partial records (Docker behaviour).

## Orchestrator decisions (fix round 1)
- Fix: SEC-1 (cap message at 16 KiB with a truncation marker), SEC-2/STAFF-1/VER-1 (read with `bufio.Reader`, discard the rest of an over-long line, always drain), plus the cheap lows in the same code: SEC-3 (`checkName`), SEC-4/STAFF-3 (WaitDelay + per-Write timeout), STAFF-2 (per-stream token), STAFF-5 second half (Stop test asserts the final system entry).
- Backlog: STAFF-4 (Task 20/26), STAFF-5 first half (Warn flood), SEC-5 (Plan 2 logs API/UI), T11 STAFF-2 TxPipelined stays in backlog (internal/logs, not this task), VER-2 note.
