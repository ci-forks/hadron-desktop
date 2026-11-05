#!/usr/bin/env bash
# Switch a CI runner's Docker to the containerd image store.
#
# The default `docker` builder can only export a registry build cache
# (`make CACHE_PUSH=true`) from the containerd store; the classic store fails
# the cache export. Run it after the runner's disk cleanup and before any pull,
# because images already in the classic store are not visible after the switch.
# Settings already in daemon.json are kept.
set -euo pipefail

conf=/etc/docker/daemon.json
current="$(sudo cat "$conf" 2>/dev/null || echo '{}')"
jq '.features["containerd-snapshotter"] = true' <<<"$current" | sudo tee "$conf" >/dev/null
sudo systemctl restart docker

if ! docker info --format '{{json .DriverStatus}}' | grep -q io.containerd.snapshotter; then
    echo "Docker did not switch to the containerd image store:" >&2
    docker info >&2
    exit 1
fi
echo "Docker uses the containerd image store."
