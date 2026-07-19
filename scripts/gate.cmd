@echo off
REM gate.cmd - the full quality gate (Windows-native twin of gate.sh).
REM Usage: scripts\gate.cmd            (set SKIP_RACE=1 for a quick no-race run)
REM Steps: gofmt -l (fail if any output), go build, go vet, tests (-race needs
REM CGO: C:\dev\mingw64\bin is put on PATH), golangci-lint if installed.
setlocal
cd /d "%~dp0.."

echo ==^> gofmt
set "GOFMT_OUT=%TEMP%\churn-gofmt-%RANDOM%.txt"
gofmt -l cmd internal web\embed.go > "%GOFMT_OUT%"
if errorlevel 1 (del "%GOFMT_OUT%" & exit /b 1)
for %%A in ("%GOFMT_OUT%") do set GOFMT_SIZE=%%~zA
if "%GOFMT_SIZE%"=="0" goto :fmt_ok
type "%GOFMT_OUT%"
del "%GOFMT_OUT%"
echo gate: gofmt found unformatted files ^(run: gofmt -w ^<file^>^)
exit /b 1
:fmt_ok
del "%GOFMT_OUT%"

echo ==^> go build ./...
go build ./...
if errorlevel 1 exit /b 1

echo ==^> go vet ./...
go vet ./...
if errorlevel 1 exit /b 1

if "%SKIP_RACE%"=="1" goto :quick_test

echo ==^> go test -race ./...  ^(CGO via C:\dev\mingw64\bin^)
set "PATH=C:\dev\mingw64\bin;%PATH%"
set "CGO_ENABLED=1"
go test -race ./...
if errorlevel 1 exit /b 1
goto :lint

:quick_test
echo ==^> go test ./...  ^(SKIP_RACE=1: no race detector^)
go test ./...
if errorlevel 1 exit /b 1

:lint
where golangci-lint >nul 2>nul
if errorlevel 1 goto :no_lint
echo ==^> golangci-lint run ./...
golangci-lint run ./...
if errorlevel 1 exit /b 1
goto :done

:no_lint
echo ==^> golangci-lint not installed - SKIPPED ^(install it for the full gate^)

:done
echo ==^> gate green
