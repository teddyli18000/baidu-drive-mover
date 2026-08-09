# Architecture Plan

## Overview

BaiduDriveMover is a local-first Windows migration pipeline:

Baidu Share
→ scan manifest
→ staged transfer to user's Baidu account
→ local bounded cache
→ Google Drive upload
→ verification
→ cleanup

## Runtime layout

Only executable directory is allowed:

```
BaiduDriveMover.exe

temp/
  state.db
  logs/
  cache/
  chrome-profile/
  rclone/
  tasks/
```

No other persistent locations are allowed.

## Components

### Core Orchestrator

Responsibilities:

- task lifecycle
- pipeline scheduling
- recovery after restart
- resource limits
- logging

### Baidu Adapter

Responsibilities:

- authentication
- share parsing
- extraction code handling
- recursive tree scan
- batch transfer
- download

Existing mature implementations may be reused where possible.

### Google Drive Adapter

Responsibilities:

- OAuth initialization
- create task root folder
- upload
- verify

### State Database

SQLite state machine.

File states:

```
DISCOVERED
BAIDU_STAGED
LOCAL_READY
DRIVE_UPLOADING
DRIVE_VERIFIED
CLEANED
DONE
FAILED
```

## Pipeline

Producer/consumer design:

```
scan
 ↓
batch planner
 ↓
baidu staging queue
 ↓
download worker
 ↓
drive upload worker
 ↓
verify worker
 ↓
cleanup worker
```

## Security boundary

The tool is trusted only on the local machine.

Required:

- no cloud service owned by this project
- no external telemetry
- no hidden background service
- no automatic startup
- no unrelated file access

## Development phases

M0 Documentation and repository rules

M1 CLI skeleton and temp directory manager

M2 Baidu authentication and share scanner

M3 Correct recursive batch transfer

M4 Google Drive integration

M5 Pipeline and SQLite recovery

M6 Packaging and real-world testing
