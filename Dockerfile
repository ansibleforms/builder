# The AnsibleForms RTE builder : builds the RTE images an AnsibleForms controller defines and pushes
# them to their registry. One Go binary, which also signs (no cosign binary), and a rootless
# BuildKit, on Alpine. No python, no ansible : what it builds brings its own.
#
#   docker run -d --name af-builder \
#     --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
#     -e CONTROLLER_URL=https://forms.example.com -e RTE_TOKEN=<at least 16 characters> \
#     -e RTE_JOIN_TOKEN=afj_... -e RTE_NAME=builder-1 \
#     -v af-builder:/home/node/work ghcr.io/ansibleforms/ansibleforms-rte-builder
#
# The two security options let its rootless buildkitd make the user namespaces it builds in ;
# with BUILDKIT_HOST naming another BuildKit they are not needed.

# BuildKit's rootless binaries (buildkitd, rootlesskit, runc), pinned like the base ; Dependabot
# proposes new pins. buildctl is left out : the builder speaks to buildkitd with its Go client
FROM moby/buildkit:v0.35.1-rootless AS buildkit

# the builder, compiled once on the build host for each target architecture (no emulation)
FROM --platform=$BUILDPLATFORM golang:1.27-trixie@sha256:430985542100b871e108ccc00e8dcc5eef08ca14ee5825e98ebf563d8ffec4ee AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/ansibleforms-rte-builder .

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

ARG VERSION=
LABEL org.opencontainers.image.source="https://github.com/ansibleforms/builder" \
      org.opencontainers.image.url="https://ansibleforms.com" \
      org.opencontainers.image.title="AnsibleForms RTE builder" \
      org.opencontainers.image.description="Builds the RTE images an AnsibleForms controller defines, and pushes them to their registry" \
      org.opencontainers.image.licenses="GPL-3.0" \
      org.opencontainers.image.version="${VERSION}"

# tini as PID 1 ; shadow-uidmap's newuidmap and newgidmap, which rootlesskit maps the user's
# subordinate ids with, given the one capability each needs instead of setuid root (a setuid
# newuidmap is refused writing the map in a container) ; fuse-overlayfs for a kernel whose
# overlayfs a user namespace may not mount. The user keeps the name node and the uid 1000 of the
# Node.js images before, so an existing volume at /home/node/work still fits.
RUN apk upgrade --no-cache \
 && apk add --no-cache tini ca-certificates shadow-uidmap fuse3 fuse-overlayfs libcap-setcap \
 && chmod u-s /usr/bin/newuidmap /usr/bin/newgidmap \
 && setcap cap_setuid+ep /usr/bin/newuidmap \
 && setcap cap_setgid+ep /usr/bin/newgidmap \
 && apk del --no-cache libcap-setcap \
 && addgroup -g 1000 node \
 && adduser -D -u 1000 -G node -s /bin/sh node \
 && echo "node:100000:65536" > /etc/subuid \
 && echo "node:100000:65536" > /etc/subgid

COPY --from=buildkit /usr/bin/buildkitd /usr/bin/rootlesskit /usr/bin/buildkit-runc /usr/local/bin/
COPY --from=build /out/ansibleforms-rte-builder /usr/local/bin/ansibleforms-rte-builder

# its spool, its builds' files and its buildkitd's cache : keep them on a volume. USER : how
# buildkitd knows it runs rootless ; XDG_RUNTIME_DIR : its run files
RUN mkdir -p /home/node/work/run && chown -R node:node /home/node/work && chmod 700 /home/node/work/run
ENV USER=node \
    HOME=/home/node \
    BUILDER_WORK_PATH=/home/node/work \
    XDG_RUNTIME_DIR=/home/node/work/run
USER node
WORKDIR /home/node
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["ansibleforms-rte-builder", "health"]
ENTRYPOINT ["/sbin/tini", "--", "ansibleforms-rte-builder"]
