#!/usr/bin/env bash
# Generic docker-in-docker test harness.
#
# Builds a dind image variant from the repo Dockerfile, starts its docker daemon over a
# local TCP port, and runs a Go test command against that daemon via DOCKER_HOST. This
# validates the actual docker version and behaviour shipped in the dind image, so any
# daemon-level regression surfaces here (e.g. the "docker cp" break in gitea/runner#981).
# It is deliberately generic: point it at any package/test to exercise the dind daemon.
#
# Usage: scripts/test-dind.sh [target] [-- go-test-args...]
#   target:        dind (default) or dind-rootless
#   go-test-args:  passed verbatim to `go test`. The default exercises the daemon-facing tests
#                  that need no registry access (a fresh daemon, e.g. on fork-PR CI, can't
#                  authenticate pulls): the env-extraction build (FROM scratch) and the #981
#                  /var/run symlink copy regression (which reuses a preloaded alpine).
#
# Env:
#   DIND_TEST_PORT     host port for the daemon (default 32375)
#   DIND_TEST_IMAGE    skip the build and use this prebuilt image instead
#   DIND_TEST_PRELOAD  space-separated images to copy from the host daemon into the fresh one
set -euo pipefail

target="dind"
case "${1:-}" in
  dind|dind-rootless) target="$1"; shift ;;
esac
[ "${1:-}" = "--" ] && shift
[ $# -eq 0 ] && set -- -race -run '^TestDocker$|^TestDockerCopyToSymlinkPath$' ./act/container/

port="${DIND_TEST_PORT:-32375}"
name="gitea-runner-dind-test-$$"
image="${DIND_TEST_IMAGE:-gitea-runner-${target}:dind-test}"
# The host daemon endpoint, captured before DOCKER_HOST is pointed at the fresh dind daemon.
host_docker="${DOCKER_HOST:-unix:///var/run/docker.sock}"

cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT

if [ -z "${DIND_TEST_IMAGE:-}" ]; then
  echo "==> Building ${target} image"
  docker build --target "$target" -t "$image" .
fi

# Override the image entrypoint (s6) and run only dockerd, exposed over insecure TCP.
# We are testing the daemon the image ships, not the runner supervision tree.
#
# How the test process reaches the daemon depends on where it runs:
#   - plain host: publish 2375 on loopback and connect to 127.0.0.1.
#   - inside a container (CI), the daemon is a sibling container, so its published port is on
#     the host, not our loopback; instead attach it to our own network and reach it by name.
self_container=""
if [ -f /.dockerenv ]; then
  self_container="$(cat /proc/sys/kernel/hostname 2>/dev/null || cat /etc/hostname)"
fi
self_network=""
if [ -n "$self_container" ]; then
  self_network="$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{"\n"}}{{end}}' "$self_container" 2>/dev/null | head -1)"
fi

# The two cases differ only in how the daemon is exposed and addressed; everything else
# (privileged, name, TLS-off entrypoint, image, --host) is shared, so collect just the
# differing run args and the resulting DOCKER_HOST here.
if [ -n "$self_network" ]; then
  echo "==> Starting ${target} daemon on network ${self_network} (reached as ${name}:2375)"
  run_args=(--network "$self_network")
  daemon_host="tcp://${name}:2375"
else
  echo "==> Starting ${target} daemon on tcp://127.0.0.1:${port}"
  run_args=(-p "127.0.0.1:${port}:2375")
  daemon_host="tcp://127.0.0.1:${port}"
fi
# Create the dind container on the host daemon first, then repoint DOCKER_HOST at it: exporting
# DOCKER_HOST before `docker run` would make this `docker run` target the not-yet-existent dind.
docker run -d --privileged --name "$name" "${run_args[@]}" \
  -e DOCKER_TLS_CERTDIR= \
  --entrypoint dockerd-entrypoint.sh \
  "$image" --host=tcp://0.0.0.0:2375 >/dev/null
export DOCKER_HOST="$daemon_host"

echo "==> Waiting for daemon"
for _ in $(seq 1 60); do
  docker version --format 'server docker {{.Server.Version}}' 2>/dev/null && break
  sleep 1
done

# Seed the fresh daemon with images the host already has (the CI job pulls them in the
# preceding `make test`), so the daemon-facing tests run without registry access.
echo "==> Seeding daemon with cached host images"
for img in ${DIND_TEST_PRELOAD:-alpine:latest}; do
  if docker -H "$host_docker" image inspect "$img" >/dev/null 2>&1; then
    docker -H "$host_docker" save "$img" | docker load >/dev/null 2>&1 && echo "  loaded $img" || true
  fi
done

echo "==> Running tests against dind daemon"
go test "$@"
