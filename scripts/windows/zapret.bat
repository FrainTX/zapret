@echo off
rem Prosvet: double-click to open the panel.
rem First run: installs the service (Windows asks for administrator rights)
rem and tunes strategies, then opens the panel. Later runs: just the panel.
setlocal
cd /d "%~dp0"
chcp 65001 >nul
title Prosvet
if not defined WT_SESSION mode con: cols=132 lines=42
sc query prosvet >nul 2>&1
if errorlevel 1 (
  "%~dp0prosvet.exe" install
)
rem The installed copy updates itself; prefer it over this folder's copy.
set "EXE=%~dp0prosvet.exe"
if exist "%ProgramFiles%\Prosvet\prosvet.exe" set "EXE=%ProgramFiles%\Prosvet\prosvet.exe"
"%EXE%"
endlocal
