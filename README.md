# Library Management System

Local frontend-backend separated library management system. The Go service exposes JSON APIs and the React application in `web/` is developed independently through Vite.

## Prerequisites

- Go 1.26 or newer
- MySQL 8.4 running on `127.0.0.1:3306`
- Node.js 24 or newer with npm
- The local MySQL secrets created by the setup process in
  `%USERPROFILE%\mysql\secrets`

## Local commands

Run commands from this project directory in PowerShell.

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\tools\doctor.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\tools\migrate.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\tools\dev.ps1
```

Start all local services as background processes that remain available after the
command window is closed:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\tools\start-local.ps1
```

The API listens on `http://127.0.0.1:8080` by default. In a second PowerShell window, start the independent React frontend:

```powershell
npm.cmd --prefix .\web install
powershell -NoProfile -ExecutionPolicy Bypass -File .\tools\dev-web.ps1
```

Open `http://127.0.0.1:5173`. Vite proxies `/api` to the Go API during development.

Create the first administrator once, after migrations have run. The script prompts for the password securely and does not store it in the repository.

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\tools\create-admin.ps1 -Username admin
```

```powershell
Invoke-RestMethod http://127.0.0.1:8080/healthz
Invoke-RestMethod http://127.0.0.1:8080/readyz
```

## Verification

The smoke test creates isolated temporary data, exercises the core API flow,
then removes that data and stops its temporary API process.

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\tools\smoke.ps1
```

## Demo test data

After importing book metadata, create local test data for circulation pages.
The script preserves imported books and creates only `DEMO-` prefixed accounts,
readers and copies. It is safe to run again: existing demo data is not duplicated.

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\tools\seed-demo.ps1
```

The demo accounts use `DemoPass123!` by default. Pass `-DemoPassword` to use a
different local test password. Demo accounts are administrator/staff accounts;
readers are deliberately stored only as reader profiles.

MySQL lifecycle

The local MySQL instance is not installed as a Windows service. Use these
scripts when it is not already running.

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\tools\start-mysql.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\tools\stop-mysql.ps1
```

## Database roles

- `library_app`: application runtime access. It can only read and modify data
  in `library_management`.
- `library_migrate`: local schema migration access. It can perform DDL only in
  `library_management`.
- `library_admin`: local MySQL maintenance account. Its password is stored with
  Windows DPAPI outside this repository.

No password is stored in this repository or in `.env` files.

## Project layout

```text
cmd/api/          HTTP server entry point
cmd/migrate/      migration command entry point
internal/config/  environment-based configuration
internal/database/ MySQL connection helper
internal/migration/ migration runner
migrations/       ordered SQL migrations
tools/            local Windows development scripts
web/              independent React + TypeScript + Vite frontend
```
