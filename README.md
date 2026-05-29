# Gitea Runner

## Installation

### Prerequisites

Docker Engine Community version is required for docker mode. To install Docker CE, follow the official [install instructions](https://docs.docker.com/engine/install/).

### Download pre-built binary

Visit [here](https://dl.gitea.com/gitea-runner/) and download the right version for your platform.

### Build from source

```bash
make build
```

### Build a docker image

```bash
make docker
```

## Quickstart

Actions are disabled by default, so you need to add the following to the configuration file of your Gitea instance to enable it:

```ini
[actions]
ENABLED=true
```

### Register

```bash
./gitea-runner register
```

And you will be asked to input:

1. Gitea instance URL, like `http://192.168.8.8:3000/`. You should use your gitea instance ROOT_URL as the instance argument
 and you should not use `localhost` or `127.0.0.1` as instance IP;
2. Runner token, you can get it from `http://192.168.8.8:3000/admin/actions/runners`;
3. Runner name, you can just leave it blank;
4. Runner labels, you can just leave it blank.

The process looks like:

```text
INFO Registering runner, arch=amd64, os=darwin, version=0.1.5.
WARN Runner in user-mode.
INFO Enter the Gitea instance URL (for example, https://gitea.com/):
http://192.168.8.8:3000/
INFO Enter the runner token:
fe884e8027dc292970d4e0303fe82b14xxxxxxxx
INFO Enter the runner name (if set empty, use hostname: Test.local):

INFO Enter the runner labels, leave blank to use the default labels (comma-separated, for example, ubuntu-latest:docker://docker.gitea.com/runner-images:ubuntu-latest):

INFO Registering runner, name=Test.local, instance=http://192.168.8.8:3000/, labels=[ubuntu-latest:docker://docker.gitea.com/runner-images:ubuntu-latest ubuntu-22.04:docker://docker.gitea.com/runner-images:ubuntu-22.04 ubuntu-20.04:docker://docker.gitea.com/runner-images:ubuntu-20.04].
DEBU Successfully pinged the Gitea instance server
INFO Runner registered successfully.
```

You can also register with command line arguments.

```bash
./gitea-runner register --instance http://192.168.8.8:3000 --token <my_runner_token> --no-interactive
```

If the registry succeed, it will run immediately. Next time, you could run the runner directly.

### Run

```bash
./gitea-runner daemon
```

### Run with docker

```bash
docker run -e GITEA_INSTANCE_URL=https://your_gitea.com -e GITEA_RUNNER_REGISTRATION_TOKEN=<your_token> -v /var/run/docker.sock:/var/run/docker.sock --name my_runner gitea/runner:nightly
```

Mount a volume on `/data` if you want the registration file and optional config to survive container recreation (see [scripts/run.sh](scripts/run.sh)).

### Configuration

The runner is configured with a YAML file. Generate a starting point (this matches what ships in the tree):

```bash
./gitea-runner generate-config > config.yaml
```

Pass it with `-c` / `--config` on any command that loads configuration (`register`, `daemon`, `cache-server`):

```bash
./gitea-runner -c config.yaml register
./gitea-runner -c config.yaml daemon
./gitea-runner -c config.yaml cache-server
```

Every option is described in [config.example.yaml](internal/pkg/config/config.example.yaml) (the same content `generate-config` prints).

#### Without a config file

If you omit `-c`, built-in defaults apply (same as an empty YAML document). A small set of **deprecated** environment variables can still override parts of that default config, but **only when no `-c` path was given**; they are ignored if you use a config file:

| Variable | Effect |
| --- | --- |
| `GITEA_DEBUG` | If true, sets log level to `debug` |
| `GITEA_TRACE` | If true, sets log level to `trace` |
| `GITEA_RUNNER_CAPACITY` | Concurrent jobs (integer) |
| `GITEA_RUNNER_FILE` | Registration state file path (default `.runner`) |
| `GITEA_RUNNER_ENVIRON` | Extra job env vars as comma-separated `KEY:VALUE` pairs |
| `GITEA_RUNNER_ENV_FILE` | Path to an env file merged into job env (same idea as `runner.env_file` in YAML) |

Prefer a YAML file for all settings.

#### Registration vs config labels

If `runner.labels` is set in the YAML file, those labels are used during `register` and the `--labels` CLI flag is ignored.

#### External cache (`actions/cache`)

If `cache.external_server` is set, you must set `cache.external_secret` to the same value on this runner and on the standalone cache server. Run the server with `gitea-runner cache-server` using a config that defines `cache.external_secret` (and matching `cache.dir` / host / port as needed). Flags `--dir`, `--host`, and `--port` on `cache-server` override the file.

#### Official Docker image

Besides `GITEA_INSTANCE_URL` and `GITEA_RUNNER_REGISTRATION_TOKEN`, the image entrypoint supports optional variables such as `CONFIG_FILE` (passed through as `-c`), `GITEA_RUNNER_LABELS`, `GITEA_RUNNER_EPHEMERAL`, `GITEA_RUNNER_ONCE`, `GITEA_RUNNER_NAME`, `GITEA_MAX_REG_ATTEMPTS`, `RUNNER_STATE_FILE`, and `GITEA_RUNNER_REGISTRATION_TOKEN_FILE`. See [scripts/run.sh](scripts/run.sh) for exact behavior.

For a fuller container-oriented walkthrough, see [examples/docker](examples/docker/README.md).

When `container.bind_workdir` is enabled, stale task workspace directories can be cleaned while the runner is idle:
- directories older than `runner.workdir_cleanup_age` are removed (default: `24h`; set `0` to disable)
- cleanup runs every `runner.idle_cleanup_interval` (default: `10m`; set `0` to disable)
- only purely numeric subdirectories under `container.workdir_parent` are treated as task workspaces and may be removed
- cleanup assumes `container.workdir_parent` is not shared across multiple runners

### Example Deployments

Check out the [examples](examples) directory for sample deployment types.
