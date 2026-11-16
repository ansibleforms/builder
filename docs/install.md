# Installing the RTE builder

## Table of contents

- [Before you start](#before-you-start)
- [Docker](#docker)
- [Docker Compose](#docker-compose)
- [Kubernetes](#kubernetes)
- [Settings](#settings)

---

## Before you start

A builder needs to reach the controller (it dials it) and the registries it pushes to; nothing needs to reach it.

1. In the controller's Designer, open **RTE builders** and click **+** for a join token.
2. Choose the builder's own token, `RTE_TOKEN`: at least 16 characters, kept by the controller once it joins.

---

## Docker

The builder runs its own rootless BuildKit, which needs two security options to make its user namespaces.

```bash
docker run -d --name af-builder --restart unless-stopped \
  --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
  -e CONTROLLER_URL=https://forms.example.com \
  -e RTE_TOKEN=<at least 16 characters> -e RTE_JOIN_TOKEN=<the join token> \
  -e RTE_NAME=builder-1 -v af-builder:/home/node/work \
  ghcr.io/ansibleforms/ansibleforms-rte-builder
```

---

## Docker Compose

The same as a service, its work folder on a volume so BuildKit's cache survives a restart.

```yaml
services:
  builder:
    image: ghcr.io/ansibleforms/ansibleforms-rte-builder
    restart: unless-stopped
    security_opt: [seccomp=unconfined, apparmor=unconfined]
    environment:
      CONTROLLER_URL: https://forms.example.com
      RTE_TOKEN: ${RTE_TOKEN}
      RTE_JOIN_TOKEN: ${RTE_JOIN_TOKEN}
      RTE_NAME: builder-1
    volumes:
      - builder:/home/node/work
volumes:
  builder:
```

---

## Kubernetes

A Deployment with one replica per builder; a pod security context that allows its user namespaces, or a BuildKit of its own.

Where the security options are not allowed, run BuildKit as its own service (the BuildKit project's Helm chart or manifests)
and point the builder at it with `BUILDKIT_HOST=tcp://buildkitd:1234`; the builder then needs no special option.

---

## Settings

Each setting is an environment variable; only the first two are required.

| Variable | What it sets |
|---|---|
| `CONTROLLER_URL` | the controller's address, as people open it |
| `RTE_TOKEN` | its own token, at least 16 characters |
| `RTE_JOIN_TOKEN` | a join token that approves it at once, the first time |
| `RTE_NAME` | its name in RTE builders (default `builder-<hostname>-<port>`) |
| `CONTROLLER_TLS_VERIFY` | `0` accepts the controller's self-signed certificate, in one trusted network only |
| `BUILDKIT_HOST` | another BuildKit to build with; empty, its own rootless one |
| `BUILDER_WORK_PATH` | its spool, its builds' files and BuildKit's cache (`/home/node/work`) |
| `HTTPS_PROXY`, `NO_PROXY` | the proxy it dials the controller through |
| `PORT` | its `/live` and `/ready` (8000) |
| `LOG_LEVEL` | debug, info, notice (default), warning or error |
