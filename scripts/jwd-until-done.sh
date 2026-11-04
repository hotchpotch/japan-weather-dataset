#!/usr/bin/env bash
# Runs a resumable jwd command (backfill, probe) until it finishes, restarting
# it after a cool-down when it stops for a reason that can pass by itself.
#
#   scripts/jwd-until-done.sh ./bin/jwd -data-dir tmp/data backfill -frequency daily
#
# Exit codes of jwd:
#   0          done                      -> stop
#   130        interrupted (Ctrl+C)      -> stop
#   4, 5       blocked / maintenance     -> stop; a person has to check JMA first
#   3          still rate limited after jwd's own waits (~1 h) -> wait, restart
#   6          own 24 h request quota    -> wait, restart
#   1 (other)  network errors, repeated server errors -> wait, restart
#
# The cool-down starts at JWD_COOLDOWN (default 15 min), doubles after each
# failed run up to JWD_COOLDOWN_MAX (default 4 h), and resets after a run
# that lasted longer than JWD_PROGRESS_SECS (default 1 h). After
# JWD_MAX_FAILURES (default 8) consecutive short failed runs the script gives
# up, so that a persistent error (e.g. a changed CSV layout) does not keep
# sending requests.
set -u

cooldown_base=${JWD_COOLDOWN:-900}
cooldown_max=${JWD_COOLDOWN_MAX:-14400}
progress_secs=${JWD_PROGRESS_SECS:-3600}
max_failures=${JWD_MAX_FAILURES:-8}

log() { echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') jwd-until-done: $*" >&2; }

trap 'log "interrupted"; exit 130' INT TERM

cooldown=$cooldown_base
failures=0
while :; do
	start=$(date +%s)
	log "start: $*"
	"$@"
	code=$?
	elapsed=$(($(date +%s) - start))
	log "exit=$code after ${elapsed}s"
	case $code in
	0) log "done"; exit 0 ;;
	130) exit 130 ;;
	4 | 5) log "access blocked or maintenance; check JMA before resuming"; exit "$code" ;;
	esac
	if ((elapsed > progress_secs)); then
		cooldown=$cooldown_base
		failures=0
	fi
	failures=$((failures + 1))
	if ((failures > max_failures)); then
		log "giving up after $max_failures consecutive failed runs"
		exit "$code"
	fi
	log "restarting in ${cooldown}s (failure $failures/$max_failures)"
	sleep "$cooldown" &
	wait $!
	cooldown=$((cooldown * 2 > cooldown_max ? cooldown_max : cooldown * 2))
done
