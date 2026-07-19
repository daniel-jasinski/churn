@echo off
REM web-build.cmd - rebuild the frontend bundle (Windows-native twin of web-build.sh).
REM Usage: scripts\web-build.cmd
REM Runs `npm ci` if web\node_modules is missing, then the production build.
REM Commit web\dist together with the src change (freshness test enforces it).
setlocal
cd /d "%~dp0..\web"

if not exist node_modules (
  echo ==^> web\node_modules missing: npm ci
  call npm ci
  if errorlevel 1 exit /b 1
)

echo ==^> npm run build (esbuild -^> web\dist)
call npm run build
if errorlevel 1 exit /b 1
echo ==^> done - remember to commit web\dist with your web\src change
