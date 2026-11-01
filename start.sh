#!/bin/sh
# ARTEX supervisor launch script (Linux / macOS / Docker ENTRYPOINT)
#
# Usage:
#   ./start.sh                            run in the foreground (Ctrl-C to stop)
#   nohup ./start.sh >artex.log 2>&1 &    run in the background
#   ./start.sh -addr :9000                extra args are passed through to artex
#
# It does one thing: run artex, and when the process exits decide from the exit
# code whether to start it again.
#
#   0      normal stop by the user   -> exit the loop
#   75     restart requested         -> re-run immediately (e.g. applying a
#                                       staged update or rollback)
#   other  crash                     -> back off and re-run (1->2->4..., max 60s)
#
# Deliberately no downloading, SHA256 verification, or binary swapping here:
# that logic would have to be written twice (sh and bat), and it is the part
# that must never fail — a broken replacement binary would just be relaunched
# forever. So all of it lives in Go (the selfupdate package) and runs inside
# artex at startup; this script stays dumb on purpose.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] executable not found: $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Forward stop signals to artex itself.
#
# Required under Docker: `docker stop` sends SIGTERM only to PID 1 (this
# script), not to the child. Without forwarding, artex never receives the
# signal, cannot shut down gracefully, and is SIGKILLed 10s later — cutting any
# running task off mid-flight.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# A signal interrupts wait and makes it return >128. At that point the child
	# is still shutting down gracefully, so wait again to get its real exit code.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] stopped"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] exited normally"
			exit 0
			;;
		"$RESTART_CODE")
			# Update/rollback is staged: on re-run artex applies it at startup
			# (see selfupdate.Bootstrap).
			echo "[artex] restart requested, applying new version..."
			delay=1
			;;
		*)
			echo "[artex] abnormal exit (code=$code), restarting in ${delay}s" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
