@echo off
rem Switch the console to UTF-8 so output is not garbled.
chcp 65001 >nul 2>&1
rem ARTEX supervisor launch script (Windows)
rem
rem Usage:
rem   start.bat                  run in the foreground (Ctrl-C to stop)
rem   start.bat -addr :9000      extra args are passed through to artex
rem
rem It does one thing: run artex.exe, and when the process exits decide from the
rem exit code whether to start it again.
rem
rem   0      normal stop by the user  -> exit the loop
rem   75     restart requested        -> re-run immediately (applying a staged
rem                                      update or rollback)
rem   other  crash                    -> back off and re-run (1->2->4..., max 60s)
rem
rem Downloading, SHA256 verification, and binary swapping are not here — all of
rem that is done by artex itself at startup (the selfupdate package). The script
rem stays dumb on purpose; see the notes at the top of start.sh.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] executable not found: %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] exited normally
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem Update/rollback is staged: on re-run artex applies it at startup.
	echo [artex] restart requested, applying new version...
	set /a delay=1
	goto loop
)

echo [artex] abnormal exit ^(code=!code!^), restarting in !delay!s 1>&2
rem timeout fails when the console is redirected, so use ping as a fallback
rem (an N-second delay needs N+1 pings).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
