# The Linux that RocketClaw's make targets run in on a Mac (see the Makefile), set up like CI: Ubuntu, the Go of
# go.mod, CI's tools and ripgrep, Playwright's Chromium for the browser tests, and what the emulated shell's
# acceptance tests need. Targets run as dev, with the Mac user's uid, so files stay theirs and permission
# checks behave as on CI.
FROM docker.io/library/ubuntu:26.04

ARG GO_VERSION
ARG UID
ARG BUN_VERSION=1.4.4
ARG PLAYWRIGHT_VERSION=1.63.0
ARG RIPGREP_VERSION=14.1.0

RUN apt-get update && apt-get install -y --no-install-recommends \
      arch-test bubblewrap ca-certificates cloc curl docker.io gcc git jq libc6-dev make mmdebstrap nodejs procps \
      uidmap unzip \
    && rm -rf /var/lib/apt/lists/*

ENV PATH=/usr/local/go/bin:/home/dev/go/bin:$PATH \
    PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    ROCKETCLAW_PLAYWRIGHT_MODULE=/opt/playwright/node_modules/playwright-core \
    ROCKETCLAW_CHROMIUM=/usr/local/bin/chromium-for-tests

RUN arch=$(dpkg --print-architecture) \
    && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" | tar -xz -C /usr/local \
    && curl -fsSL https://bun.sh/install | BUN_INSTALL=/usr/local bash -s "bun-v${BUN_VERSION}" \
    && rg=$([ "$arch" = arm64 ] && echo aarch64-unknown-linux-gnu || echo x86_64-unknown-linux-musl) \
    && curl -fsSL "https://github.com/BurntSushi/ripgrep/releases/download/${RIPGREP_VERSION}/ripgrep-${RIPGREP_VERSION}-${rg}.tar.gz" \
      | tar -xz -C /usr/local/bin --strip-components 1 "ripgrep-${RIPGREP_VERSION}-${rg}/rg" \
    && mkdir /opt/playwright && cd /opt/playwright && bun add "playwright-core@${PLAYWRIGHT_VERSION}" \
    && node node_modules/playwright-core/cli.js install --with-deps --no-shell chromium \
    && ln -s "$(node -e 'console.log(require("playwright-core").chromium.executablePath())')" "$ROCKETCLAW_CHROMIUM"

RUN userdel --remove ubuntu \
    && useradd --uid "$UID" --create-home --shell /bin/bash dev \
    && usermod --add-subuids 100000-165535 --add-subgids 100000-165535 dev \
    && install -d -o dev -g dev /sandbox

ENV HOME=/home/dev

# As root, the one thing dev cannot do: give it the container's cgroup tree, in which the emulated shell's
# tests make their own (the container's processes move to a leaf first), and the Docker socket, for the tests'
# PostgreSQL. Then the target runs as dev.
ENTRYPOINT ["/bin/sh", "-c", "cg=/sys/fs/cgroup && mkdir -p $cg/init $cg/dev && for p in $(cat $cg/cgroup.procs); do echo $p > $cg/init/cgroup.procs 2>/dev/null || true; done && echo '+cpu +memory +pids' > $cg/cgroup.subtree_control && chown dev $cg/dev $cg/dev/cgroup.procs $cg/dev/cgroup.subtree_control $cg/dev/cgroup.threads && echo $$ > $cg/dev/cgroup.procs && chmod 666 /var/run/docker.sock && exec setpriv --reuid=dev --regid=dev --init-groups -- \"$@\"", "sh"]
