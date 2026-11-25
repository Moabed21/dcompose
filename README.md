# dcompose

A fast terminal UI for Docker and Docker Compose. Manage containers, compose
projects, logs, stats, images, volumes and networks from one keyboard-driven
screen. Works on Linux, macOS and Windows.

```
 ⬡ dcompose   1 Containers   2 Compose   3 Logs   4 Stats   5 Images   6 Volumes   7 Networks
```

## Features

- **Containers**: start, stop, restart, pause, kill, remove, inspect, open a shell, follow logs.
- **Compose projects**: `up`, `up --build`, `down` (optionally with volumes), build, pull,
  restart and so on, for a whole project or a single service. Projects stay listed after
  `down`, so you can bring them back up later.
- **Live logs**: follows one container or a whole project, auto-reconnects when a container
  restarts, marks stderr lines, and supports filter, wrap, timestamps and export to a file.
- **Stats**: CPU, memory, network and disk I/O for every running container, sortable.
- **Images, volumes, networks**: browse, inspect, pull, remove and prune.
- Long operations run in the background with live output (press `T`), so the UI never freezes.
- Mouse support: click tabs and scroll with the wheel.

## Requirements

- **Docker** with the **Compose v2 plugin** (`docker compose`):
  [Docker Desktop](https://docs.docker.com/get-docker/) on macOS and Windows, or Docker Engine
  on Linux. Colima, OrbStack, Rancher Desktop and rootless Docker work too.
- A terminal with Unicode support. On Windows, use **Windows Terminal**.

dcompose talks to the same Docker daemon as your `docker` command. It follows `DOCKER_HOST`,
`DOCKER_CONTEXT` and the current `docker context`, including remote `ssh://` contexts. If
`docker ps` works in your terminal, dcompose will work too.

## Install

### Prebuilt binary

Download the archive for your OS and CPU from the project's **Releases** page, extract it,
and put `dcompose` (`dcompose.exe` on Windows) somewhere on your `PATH`.

| OS                    | Archive              |
|-----------------------|----------------------|
| Linux (x86-64)        | `*_linux_amd64`      |
| Linux (ARM)           | `*_linux_arm64`      |
| macOS (Apple Silicon) | `*_darwin_arm64`     |
| macOS (Intel)         | `*_darwin_amd64`     |
| Windows (x86-64)      | `*_windows_amd64`    |
| Windows (ARM)         | `*_windows_arm64`    |

On macOS, a downloaded binary may be blocked by Gatekeeper the first time. Allow it with:

```sh
xattr -d com.apple.quarantine ./dcompose
```

### Build from source

Requires [Go](https://go.dev/dl/) 1.24 or newer.

```sh
git clone <repository-url> dcompose
cd dcompose
go build .          # produces ./dcompose (dcompose.exe on Windows)
# or install it into $(go env GOPATH)/bin:
go install .
```

## Usage

```sh
dcompose                          # start; a compose file in the current directory is added automatically
dcompose ./my-app                 # also register the compose project in ./my-app
dcompose path/to/compose.yaml     # or point at a specific compose file
dcompose --version
dcompose --help
```

Press `?` at any time to see the keys for the current tab.

### Keys

**Everywhere**

| Key                     | Action                                  |
|-------------------------|-----------------------------------------|
| `1`–`7`, `tab`/`shift+tab` | switch tab (or click it)             |
| `↑↓` / `j k`, `pgup/pgdn`, `g/G` | move                           |
| `/`                     | filter the current list (`esc` clears)  |
| `R`                     | refresh now (auto-refresh every 3s)     |
| `T`                     | output of the current/last background task |
| `?`                     | help                                    |
| `q` / `ctrl+c`          | quit                                    |

**Containers & Stats**

| Key   | Action                         | Key       | Action                 |
|-------|--------------------------------|-----------|------------------------|
| `s`   | start / unpause                | `e`       | open a shell (bash or sh) |
| `S`   | stop                           | `i`       | inspect (JSON)         |
| `r`   | restart                        | `l`/`enter` | follow logs          |
| `p`   | pause / unpause                | `a`       | all / running only (Containers) |
| `K`   | kill (confirms)                | `P`       | prune stopped (Containers, confirms) |
| `d`   | force remove (confirms)        | `o`       | cycle sort (Stats)     |

**Compose** (project list, and per service after `enter`)

| Key       | Action                          | Key   | Action                         |
|-----------|---------------------------------|-------|--------------------------------|
| `enter`   | open project / list services    | `b/B` | build / build `--no-cache`     |
| `u` / `U` | `up -d` / `up -d --build`       | `P`   | pull images                    |
| `s` / `S` | start / stop                    | `L`   | aggregated project logs        |
| `r` / `p` | restart / pause                 | `c`   | show resolved compose config   |
| `d` / `D` | `down` / `down --volumes`       | `o`   | add a project from a path      |
| `esc`     | back to the project list        | `x`   | forget a project that is down  |

In the services view, `d` removes the selected service's containers, and `e` `i` `l` `K` work
on its container just like in the Containers tab.

**Logs**: `[` `]` or `←` `→` switch source · `f` follow · `w` wrap · `t` timestamps ·
`/` filter · `c` clear · `r` reconnect · `E` export to `./dcompose-<name>-<time>.log`

**Images**: `n` pull new · `p` re-pull · `d`/`D` remove / force remove · `P` prune dangling ·
`A` prune all unused · `i` inspect

**Volumes**: `d` remove · `P` prune anonymous · `A` prune all unused · `i` inspect

**Networks**: `d` remove · `P` prune unused · `i` inspect

Every destructive action asks for confirmation first.

## Where data is stored

dcompose remembers the compose projects you've used, so they stay listed after `docker compose down`:

| OS      | File                                                  |
|---------|-------------------------------------------------------|
| Linux   | `~/.config/dcompose/projects.json`                    |
| macOS   | `~/Library/Application Support/dcompose/projects.json` |
| Windows | `%AppData%\dcompose\projects.json`                    |

Run `dcompose --help` to see the exact path on your machine. Deleting the file just resets the list.

## Troubleshooting

- **"Cannot connect to the Docker daemon"**: check that `docker info` works in the same
  terminal. On macOS or Windows, make sure Docker Desktop (or your VM) is running. On Linux,
  start the service (`sudo systemctl start docker`) and make sure your user can reach it (for
  example, membership in the `docker` group).
- **Compose tab shows "'compose' is not a docker command"**: install the Docker Compose v2
  plugin. The old standalone `docker-compose` v1 isn't supported.
- **Odd symbols or misaligned borders**: use a terminal and font with good Unicode coverage
  (for example Windows Terminal, iTerm2, GNOME Terminal, kitty or WezTerm).
- **Selecting text with the mouse**: hold `shift` while dragging (`option` in iTerm2).

## Development

```sh
make build      # build ./dcompose
make test       # unit tests
make vet
make it-test    # integration tests against a real daemon (uses a throwaway "dcompose-it" project)
make dist       # cross-compile release archives for all platforms into dist/
```

To publish a release on GitHub, push a tag such as `v1.0.0`. The release workflow builds
binaries for Linux, macOS and Windows and attaches them to the release.
