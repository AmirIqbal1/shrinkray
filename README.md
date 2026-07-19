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
server. The dashboard does not upload, rename, replace, move, or delete files.

Go 1.22 or newer is required to build and run the server. From a repository
clone, test it locally with:

```bash
go run ./cmd/shrinkray-server \
  --root ~/Videos \
  --shrinkray-bin ./shrinkray
```

Then open <http://127.0.0.1:8787>. The server listens only on localhost by
default.

For safe access to a remote server, keep that default and open an SSH tunnel:

```bash
ssh -L 8787:127.0.0.1:8787 user@server
```

Then open <http://127.0.0.1:8787> on the local device.

On a trusted LAN or Tailscale network, listen on all interfaces explicitly:

```bash
go run ./cmd/shrinkray-server \
  --root /media/movies \
  --listen 0.0.0.0:8787 \
  --shrinkray-bin ./shrinkray
```

**The dashboard has no authentication. Do not expose it directly to the public
internet. Use localhost, SSH tunnelling, a trusted LAN, Tailscale, or a
protected reverse proxy.** Every browsed or submitted path is resolved against
`--root`; traversal, symlink escapes, unsupported files, and existing outputs
are rejected.

Additional server flags are `--state-dir` (default
`~/.local/share/shrinkray/server`) and `--listen` (default
`127.0.0.1:8787`). The server version is independent of the Bash CLI and starts
at `shrinkray-server v0.1.0`.

## Install

Shrinkray supports Ubuntu Server and Linux Mint. The installer adds `ffmpeg`
with `apt-get` when it is missing, then installs the `shrinkray` command for
your user.

```bash
curl -fsSL https://raw.githubusercontent.com/AmirIqbal1/shinkray/main/install.sh | bash
```

Open a new terminal after installation, then check that everything is ready:

```bash
shrinkray doctor
```

To install from a clone instead:

```bash
git clone https://github.com/AmirIqbal1/shinkray.git
cd shinkray
./install.sh
```

For a system-wide installation in `/usr/local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/AmirIqbal1/shinkray/main/install.sh | bash -s -- --system
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
| `-y` | Replace an existing output without asking | off |

Run `shrinkray --help` for usage examples.

## Safety

Shrinkray never deletes or replaces the source movie. It encodes to a temporary
file ending in `.part`, validates that file with `ffprobe`, and only then moves
it to the requested output name. Failed and interrupted encodes are cleaned up.

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
