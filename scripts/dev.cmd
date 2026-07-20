@echo off
REM dev.cmd - build and run a local dev server (Windows-native twin of dev.sh).
REM Usage: scripts\dev.cmd [extra serve flags...]
REM Builds .\churn.exe, seeds the data directory with demo data IF it does not
REM exist yet, then serves it. Extra args go to `serve`.
REM
REM Two knobs, both defaulted so the plain invocation is unchanged (:8080 on
REM .\workspace):
REM   PORT            listen port
REM   CHURN_DEV_DATA  data directory
REM They exist because a workspace is held under an exclusive OS lock
REM (internal/store/lock.go), so a second dev server needs its own data
REM directory as well as its own port - otherwise it fails on the lock rather
REM than on the port, which is a much less obvious error.
setlocal
cd /d "%~dp0.."

if "%PORT%"=="" set PORT=8080
if "%CHURN_DEV_DATA%"=="" set CHURN_DEV_DATA=workspace

echo ==^> building .\churn.exe
go build -o churn.exe .\cmd\churn
if errorlevel 1 exit /b 1

if not exist "%CHURN_DEV_DATA%" (
  echo ==^> no .\%CHURN_DEV_DATA% yet: seeding the demo workspace
  .\churn.exe seed-demo --data "%CHURN_DEV_DATA%"
  if errorlevel 1 exit /b 1
) else (
  echo ==^> reusing existing .\%CHURN_DEV_DATA%
)

REM --no-open: the dev workflow manages its own browser tab; drop --no-open to
REM have serve launch one.
echo ==^> serving http://127.0.0.1:%PORT% (Ctrl-C to stop)
.\churn.exe serve --data "%CHURN_DEV_DATA%" --listen 127.0.0.1:%PORT% --no-open %*
exit /b %errorlevel%
