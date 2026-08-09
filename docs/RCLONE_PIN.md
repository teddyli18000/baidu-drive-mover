# rclone v1.74.4 Supply-Chain Pin

This file records the exact helper artifacts accepted by v0.5.

```text
Version:        v1.74.4
Release date:   2026-07-08
Archive:        rclone-v1.74.4-windows-amd64.zip
Official URL:   https://downloads.rclone.org/v1.74.4/rclone-v1.74.4-windows-amd64.zip
Archive SHA256: ef097ef9de37a57feb7d9f9c7afb34148ad3c65be8025f1d8f7f521554a701ea
Executable:     rclone-v1.74.4-windows-amd64/rclone.exe
Exe SHA256:     492648a3867dbc620188a305e05ff3216aecbf4622bf1a6b5b978ed9c939e18c
```

The archive hash is the value published by the official rclone v1.74.4 `SHA256SUMS` file.

The executable hash was derived in a credential-free GitHub Actions job by:

1. downloading the exact official HTTPS archive above;
2. verifying the archive against the pinned official SHA-256;
3. extracting the exact expected `rclone.exe` entry;
4. hashing that executable with SHA-256.

The one-shot derivation workflow is removed after the value is recorded. Production code must verify **both** the archive hash before extraction and the executable hash before any execution. `rclone version` is an additional semantic check, not the first trust check.

Changing either hash or the accepted rclone version requires an explicit dependency/design update and a fresh official-artifact verification. The application must never substitute a binary found on `%PATH%` or silently follow `latest`.

Windows acceptance: official pinned archive and executable hashes verified, and rclone v1.74.4 executed successfully on windows-latest in credential-free CI on 2026-08-09.

