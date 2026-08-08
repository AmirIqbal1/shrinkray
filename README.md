# shrinkray 📼➡️📦

Point shrinkray at a movie. It creates a smaller copy and leaves the original
alone.

## Easy guided mode

Run Shrinkray without any options:

```bash
shrinkray
```

It will ask you to choose a movie, how small you want it, and whether you need
MKV or MP4 compatibility. The guided prompts work in a local Linux Mint
terminal and over SSH on Ubuntu Server.

If you prefer an advanced direct command, provide the movie and target size:

```bash
shrinkray movie.mkv --size 700
```

Direct mode creates `movie.shrunk.mkv` by default.

## Server dashboard

The lightweight server dashboard lets another device browse movies that are
already on a headless server, inspect them, and queue Shrinkray jobs. It uses a
single encoding worker so simultaneous software encodes cannot overload a small
server. Running jobs report live pass progress, encoding speed, and ETA. The
dashboard does not upload, rename, replace, move, or delete files.

Go 1.22 or newer is required to build and run the server. From a repository
clone, test it locally with:

```bash
go run ./cmd/shrinkray-server \
  --root ~/Videos \
  --shrinkray-bin ./shrinkray
```

Then open <http://127.0.0.1:8787>. The server listens only on localhost by
default.

Repeat `--root` to expose separate media libraries while keeping one global
encoding queue:

```bash
shrinkray-server \
  --root /media/movies \
  --root /media/tv \
  --listen 127.0.0.1:8787 \
  --shrinkray-bin ./shrinkray
```

Library names are derived from directory names (`movies` becomes `Movies` and
`tv` becomes `TV`). Give them explicit dashboard names with `Name=/path`:

```bash
shrinkray-server \
  --root "Movies=/media/movies" \
  --root "TV Shows=/media/tv" \
  --shrinkray-bin ./shrinkray
```

Separate roots are safer than `--root /media`: Shrinkray can browse only the
configured Movies and TV libraries, not unrelated folders that happen to live
under `/media`. Configured roots must not overlap.

### Systemd server installation

On Ubuntu Server, Ubuntu Desktop, or Linux Mint, install the CLI, dashboard,
managed configuration, and systemd service with:

```bash
curl -fsSL \
  https://raw.githubusercontent.com/AmirIqbal1/shrinkray/main/install-server.sh \
  | sudo bash -s -- \
      --user amir \
      --root "Movies=/media/movies" \
      --root "TV Shows=/media/tv"
```

The managed service always listens on `127.0.0.1:8787` by default. Existing
Tailscale access uses a separate HTTPS listener on port `8443`; Shrinkray never
automatically claims HTTPS port `443`. Re-running the installer updates the
binaries while preserving the configured user, backend port, Tailscale HTTPS
port, and roots unless replacements are provided explicitly. Use
`--tailscale-https-port <port>` to select another unused non-443 private port,
`--source-dir /path/to/shrinkray` to build from a local checkout, or `--dry-run`
to validate without installing system files.

The safe layout on a server that already runs Coolify or another public reverse
proxy is:

```text
Coolify / public reverse proxy:
https://panel.example.com
host port 443

Shrinkray local backend:
http://127.0.0.1:8787

Shrinkray private browser URL:
https://hostname.tailnet.ts.net:8443/
```

The installer prints the server's private Tailscale dashboard URL after setup.
Do not publish private tailnet hostnames in a public repository.

Port `443` remains reserved for Coolify, Traefik, Caddy, Nginx, Apache, or
another host reverse proxy. Port `8443` is the private Tailscale HTTPS listener,
and port `8787` remains bound to localhost only. Do not use Tailscale Funnel,
do not run `tailscale serve reset`, and do not expose port `8787` through a
router. During an upgrade, the installer can migrate a clearly owned old
Shrinkray listener from Tailscale HTTPS port 443: it configures and verifies
8443 first, then removes only the old 443 listener. Ambiguous or unrelated
routes are never removed.

For access without Tailscale Serve, keep the loopback default and open an SSH
tunnel:

```bash
ssh -L 8787:127.0.0.1:8787 user@server
```

Then open <http://127.0.0.1:8787> on the local device.

The managed installer does not offer a public bind mode. For diagnostics, run:

```text
shrinkray-server-doctor
shrinkray-server-doctor --repair
systemctl status shrinkray
journalctl -u shrinkray -f
tailscale serve status
ss -ltnp | grep -E ':(443|8443|8787)\b'
```

Normal doctor mode is read-only. Repair mode can restart only the Shrinkray
service, configure its non-443 Tailscale listener, and remove an old 443
listener only when that listener clearly proxies solely to Shrinkray. It never
restarts Docker or Coolify, changes media permissions, resets Serve, or uses
Funnel.

**The dashboard has no authentication. Do not expose it directly to the public
internet. Use localhost, SSH tunnelling, a trusted LAN, Tailscale, or a
protected reverse proxy.** Every browsed or submitted path is resolved against
the selected `--root`; traversal, symlink escapes, unsupported files, and
existing outputs are rejected. Absolute configured root paths are not exposed
through the browser API.

Additional server flags are `--state-dir` (default
`~/.local/share/shrinkray/server`) and `--listen` (default
`127.0.0.1:8787`). The server version is independent of the Bash CLI; this
multi-library release is `shrinkray-server v0.2.0`.

### Persistent job history

The dashboard stores its queue and recent job history in `jobs.json` under the
configured Shrinkray state directory. Docker persists `/var/lib/shrinkray`, so
completed, failed, cancelled, and queued records survive container recreation.
Queued work is validated and restored after restart. An encode interrupted by a
restart is marked failed and must be submitted again; it is never resumed in the
middle of an encode.

Shrinkray retains the newest 250 finished jobs together with every active or
queued job. The dashboard's **Clear history** action removes completed, failed,
and cancelled records only. It never removes source movies, compressed output,
queued work, or running jobs.

### Docker home-server deployment

Docker is the recommended way to run the dashboard on a home server. Tailscale
stays installed directly on the host; it is not installed or run in the
Shrinkray container. Copy the example settings and adjust the IDs and host
paths:

```bash
cp .env.example .env
id -u
id -g
editor .env
mkdir -p /path/to/shrinkray-state /path/to/movies /path/to/tv
```

The configured user and media group must already be able to traverse, read, and
write all three host directories. Media mounts are intentionally writable
because compressed files are created beside their sources. The state and media
directories are bind-mounted, so they survive image rebuilds and container
recreation.

Manage the deployment from the repository:

```bash
scripts/shrinkray-docker.sh start
scripts/shrinkray-docker.sh status
scripts/shrinkray-docker.sh logs
scripts/shrinkray-docker.sh restart
scripts/shrinkray-docker.sh stop
```

`build` pulls current base images and builds Shrinkray. `update` runs
`git pull --ff-only`, builds a new image, recreates only the `shrinkray`
service, and verifies container and API health. If validation fails, it retags
the saved previous image and recreates the previous Shrinkray container
configuration. It never updates or restarts unrelated containers.

Use the read-only diagnostic command before repair:

```bash
scripts/shrinkray-docker.sh doctor
sudo scripts/shrinkray-docker.sh repair
```

Doctor inspects Docker, mounts, configured IDs and permissions, local health,
ports, Tailscale, and Serve routes. Repair may start or restart only Shrinkray,
restore only its configured private Serve listener, and remove a legacy port
443 listener only if it consists of exactly one route to Shrinkray. It does not
restart Docker, Coolify, Jellyfin, reset Tailscale Serve, use Funnel, or change
media ownership.

The safe port layout is:

```text
Coolify / public reverse proxy: host port 443
Tailscale Serve:               host private HTTPS port 8443
Shrinkray Docker publication:  host 127.0.0.1:8787
Shrinkray inside container:     0.0.0.0:8787
```

Docker never publishes ports 443 or 8443. The dashboard has no authentication,
so keep port 8787 loopback-only. Configure the host-side private listener after
the local health check passes:

```bash
sudo scripts/configure-tailscale.sh
```

The script refuses port 443, preserves unrelated Serve routes, and prints the
private URL using the current DNS name returned by `tailscale status --json`.
It never contains a server-specific hostname.

To migrate an existing native systemd installation, first prepare and validate
`.env`, then run:

```bash
sudo scripts/migrate-systemd-to-docker.sh
```

Migration backs up the native unit, configuration, CLI, server, and doctor
binary under `/var/backups/shrinkray-migration/`. It builds the image before
stopping `shrinkray.service`, starts and verifies Docker, configures private
HTTPS, and disables only `shrinkray.service` after all health checks pass. On
failure after the native service is stopped, it stops only the failed
Shrinkray container, restores the backups, reloads systemd, restarts the old
service, and verifies its previous local endpoint. It does not alter port 443,
other services, media, state, or unrelated Docker data.

An optional watchdog can check the container and private route every five
minutes. The repository does not enable it automatically. The supplied unit
assumes the repository is installed at `/opt/shrinkray`; edit both paths if it
is elsewhere:

```bash
sudo install -m 0644 deploy/systemd/shrinkray-watchdog.service \
  /etc/systemd/system/shrinkray-watchdog.service
sudo install -m 0644 deploy/systemd/shrinkray-watchdog.timer \
  /etc/systemd/system/shrinkray-watchdog.timer
sudo systemctl daemon-reload
sudo systemctl enable --now shrinkray-watchdog.timer
```

The watchdog checks for the container, verifies running state, container
health, and local API health, then restarts only Shrinkray if needed. It
restores only the configured port-8443 Serve route when missing and never
changes port 443 or unrelated Serve routes. Disable it with
`sudo systemctl disable --now shrinkray-watchdog.timer`.

## Install

Shrinkray supports Ubuntu Server and Linux Mint. The installer adds `ffmpeg`
with `apt-get` when it is missing, then installs the `shrinkray` command for
your user.

```bash
curl -fsSL https://raw.githubusercontent.com/AmirIqbal1/shrinkray/main/install.sh | bash
```

Open a new terminal after installation, then check that everything is ready:

```bash
shrinkray doctor
```

To install from a clone instead:

```bash
git clone https://github.com/AmirIqbal1/shrinkray.git
cd shrinkray
./install.sh
```

For a system-wide installation in `/usr/local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/AmirIqbal1/shrinkray/main/install.sh | bash -s -- --system
```

The default user installation goes to `~/.local/bin` and does not need `sudo`
unless `ffmpeg` must be installed.

## Quick start

On Ubuntu Server or Linux Mint, the basic workflow is the same:

```bash
shrinkray ~/Movies/movie.mkv
```

Choose another target size or spend more time improving compression:

```bash
shrinkray ~/Movies/movie.mkv --size 700 --quality best
```

Process one directory:

```bash
shrinkray --batch ~/Movies --size 500
```

Include its subdirectories:

```bash
shrinkray --batch ~/Movies --recursive --size 500
```

Software video encoding is CPU-intensive and may be slow, especially with
`--quality best` or the explicitly requested AV1 codec. Start with the default
HEVC mode unless you specifically need AV1.

## Options

| Flag | What it does | Default |
|---|---|---|
| `--size <MB>` | Target output size in whole megabytes | `500` |
| `--quality <fast\|good\|best>` | Trade encoding time for compression quality | `good` |
| `--codec <auto\|hevc\|av1>` | Select the video encoder | `auto` (HEVC) |
| `--container <mkv\|mp4>` | Select the output container | `mkv` |
| `--keep-all-audio` | Keep all audio tracks instead of the first one | off |
| `--output <path>` | Set a custom output for one input file | automatic |
| `--batch <dir>` | Process videos in a directory | — |
| `--recursive` | Include subdirectories with `--batch` | off |
| `--dry-run` | Show planned work without encoding | off |
| `--machine-progress` | Emit FFmpeg key/value progress for dashboards or automation | off |
| `-y` | Replace an existing output without asking | off |

Run `shrinkray --help` for usage examples.

## Safety

Shrinkray never deletes or replaces the source movie. It encodes to a temporary
file ending in `.part`, validates that file with `ffprobe`, and only then moves
it to the requested output name. Failed and interrupted encodes are cleaned up.

### Disk space safety

Before each encode, Shrinkray checks free space on the filesystem containing the
output directory. It requires the target output size, a 512 MB encoder working
allowance, and a safety reserve equal to the larger of 2 GB or 10% of that
filesystem's capacity. Queued dashboard jobs are checked again immediately
before encoding because available space can change while they wait.

While an encode is active, Shrinkray continues checking the destination. If free
space falls below the critical 512 MB floor, it stops the encoder and removes
only its `.part` output and temporary encoder files. This protection never
deletes or modifies the original movie or unrelated files.

MKV output keeps global metadata, chapters, and available subtitles. MP4 output
drops subtitles because common movie subtitle formats are not always compatible
with MP4. Audio is optional, so silent videos work too.

The target size is approximate. Shrinkray warns when the requested target is not
smaller than the source.

## Diagnostics

```bash
shrinkray doctor
```

This shows the shrinkray version and installation path, the installed `ffmpeg`
and `ffprobe` versions, and whether HEVC and AV1 software encoders are available.

## Uninstall

For a user installation:

```bash
rm ~/.local/bin/shrinkray
```

For a system installation:

```bash
sudo rm /usr/local/bin/shrinkray
```

## Licence

Shrinkray is free software licensed under the GNU General Public License,
version 3.0 (GPL-3.0).
