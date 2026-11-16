# The AnsibleForms RTE builder : builds the RTE images an AnsibleForms controller defines and pushes
# them to their registry. Node and the builder, a rootless BuildKit and cosign. No python, no
# ansible : what it builds brings its own.
#
#   docker run -d --name af-builder \
#     --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
#     -e CONTROLLER_URL=https://forms.example.com -e RTE_TOKEN=<at least 16 characters> \
#     -e RTE_JOIN_TOKEN=afj_... -e RTE_NAME=builder-1 \
#     -v af-builder:/home/node/work ghcr.io/ansibleforms/ansibleforms-rte-builder
#
# The two security options let its rootless buildkitd make the user namespaces it builds in ;
# with BUILDKIT_HOST naming another BuildKit they are not needed.

# BuildKit's rootless binaries (buildkitd, buildctl, rootlesskit) and cosign, pinned like the base ;
# Dependabot proposes new pins
FROM moby/buildkit:v0.22.0-rootless AS buildkit
FROM ghcr.io/sigstore/cosign/cosign:v2.5.0 AS cosign

FROM node:26-trixie-slim@sha256:e6bedef217677b276c591795e169840edd9096dbf13f29df8ef8d1856b8820bc AS deps
WORKDIR /app
COPY package*.json ./
RUN npm ci --omit=dev

FROM node:26-trixie-slim@sha256:e6bedef217677b276c591795e169840edd9096dbf13f29df8ef8d1856b8820bc

ARG VERSION=
LABEL org.opencontainers.image.source="https://github.com/ansibleforms/builder" \
      org.opencontainers.image.url="https://ansibleforms.com" \
      org.opencontainers.image.title="AnsibleForms RTE builder" \
      org.opencontainers.image.description="Builds the RTE images an AnsibleForms controller defines, and pushes them to their registry" \
      org.opencontainers.image.licenses="GPL-3.0" \
      org.opencontainers.image.version="${VERSION}"

# tini as PID 1 ; uidmap's newuidmap and newgidmap, which rootlesskit maps node's subordinate ids
# with, given the one capability each needs instead of setuid root (a setuid newuidmap is refused
# writing the map in a container), as the BuildKit rootless image does ; fuse-overlayfs for a
# kernel whose overlayfs a user namespace may not mount
RUN apt-get update \
 && apt-get upgrade -y \
 && apt-get install -y --no-install-recommends tini ca-certificates uidmap fuse-overlayfs libcap2-bin \
 && chmod u-s /usr/bin/newuidmap /usr/bin/newgidmap \
 && setcap cap_setuid+ep /usr/bin/newuidmap \
 && setcap cap_setgid+ep /usr/bin/newgidmap \
 && apt-get purge -y libcap2-bin \
 && rm -rf /var/lib/apt/lists/* \
 && echo "node:100000:65536" > /etc/subuid \
 && echo "node:100000:65536" > /etc/subgid

COPY --from=buildkit /usr/bin/buildkitd /usr/bin/buildctl /usr/bin/rootlesskit /usr/bin/buildkit-runc /usr/local/bin/
COPY --from=cosign /ko-app/cosign /usr/local/bin/cosign

WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY package.json index.js healthcheck.js ./
COPY src ./src

# its spool, its builds' files and its buildkitd's cache : keep them on a volume. USER : how
# buildkitd knows it runs rootless ; XDG_RUNTIME_DIR : its run files
RUN mkdir -p /home/node/work/run && chown -R node:node /home/node/work && chmod 700 /home/node/work/run
ENV USER=node \
    HOME=/home/node \
    BUILDER_WORK_PATH=/home/node/work \
    XDG_RUNTIME_DIR=/home/node/work/run
USER node
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["node", "./healthcheck.js"]
ENTRYPOINT ["/usr/bin/tini", "--", "node", "./index.js"]
