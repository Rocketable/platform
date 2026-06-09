# Quickweb

Quickweb serves static applets from the current working directory and gives each page one persistent JSON document through `/data`.

## Run

```sh
cd ./alitu-quickweb
go run github.com/Rocketable/platform/cmd/quickweb@main --db ./alitu-quickweb.sqlite --addr 0.0.0.0:8797 --service-name alitu-quickweb
```

Quickweb does not have a `--root` flag. The content root is always the process working directory.

## Flags

- `--addr`: bind address, default `0.0.0.0:8797`.
- `--db`: SQLite state database path, default `./quickweb.sqlite`.
- `--service-name`: optional human-readable name for logs and `/healthz`.
- `--base-url`: optional externally preferred URL to advertise first.

## Endpoints

- `GET /healthz`: health and diagnostics JSON.
- `GET /skills`: Markdown applet-authoring instructions.
- `GET /data?path=/applet/`: read the applet page JSON document.
- `PUT /data?path=/applet/`: full JSON overwrite.
- `POST /data?path=/applet/`: full JSON overwrite.

There is no `PATCH`. Quickweb does not merge, append, or update individual keys. Every write replaces the whole stored JSON document.

## Static Files

- `/` serves `index.html`.
- `/something/` serves `something/index.html`.
- `/something` redirects to `/something/` when `something/index.html` exists.
- SQLite files, `.env*`, `.git` internals, and dotfiles are not served.

## Systemd Example

```ini
[Unit]
Description=Quickweb instance
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/home/wallace/alitu-quickweb
ExecStart=/usr/bin/go run github.com/Rocketable/platform/cmd/quickweb@main --db /home/wallace/alitu-quickweb/alitu-quickweb.sqlite --addr 0.0.0.0:8797 --service-name alitu-quickweb
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

## Operational Notes

Quickweb is for trusted internal networks and VPN access. Do not expose it directly to the public internet without a security review.

SQLite database files are runtime state. Do not commit them, and keep them ignored in instance repositories.
