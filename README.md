# AnsibleForms RTE builder

[![CI](https://img.shields.io/github/actions/workflow/status/ansibleforms/builder/ci.yml?branch=main&label=CI)](https://github.com/ansibleforms/builder/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-GPL--3.0-blue)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-ansibleforms.com-informational)](https://ansibleforms.com)

The builder of the RTE images an [AnsibleForms](https://github.com/ansibleforms/ansibleforms) controller defines, written in Node.js.
It joins the controller, builds each image the Designer asks for with BuildKit, and pushes it to its registry.

```bash
docker run -d --name af-builder \
  --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
  -e CONTROLLER_URL=https://forms.example.com -e RTE_TOKEN=<at least 16 characters> \
  -e RTE_JOIN_TOKEN=<a join token from the Designer's RTE builders> \
  -v af-builder:/home/node/work ghcr.io/ansibleforms/ansibleforms-rte-builder
```

## Features

The controller decides what to build; the builder builds it, and knows nothing else.

- **Joins like a Runner:** dials the controller, with a join token or an admin's approval
- **Builds with BuildKit:** a rootless buildkitd of its own, or the one `BUILDKIT_HOST` names
- **Pushes to any registry:** the login comes sealed with each build and is never kept
- **Signs and lists:** cosign signatures, offline, and an SBOM pushed with the image
- **Live log:** every line of a build reaches the controller's job, its secrets masked
- **Survives a drop:** what the controller did not store yet is sent again after a reconnect
- **Needs nothing more:** no database, no repositories, no playbooks

## How it works

The RTE images are defined in the controller's Designer, which writes each one's Dockerfile.
A build is a job: the controller hands it to a builder that is online, with the Dockerfile, the reference to push to,
and the registry login and signing key sealed for that builder; the build's log and result come back as the job's.

The builder and the controller meet over the controller's runner socket, at a version of its contract.
A builder works with every controller that speaks the same contract, whatever their releases.

## Versions

The builder has its own versions, released from this repository's [Releases](https://github.com/ansibleforms/builder/releases).
Each release publishes `ghcr.io/ansibleforms/ansibleforms-rte-builder` for amd64 and arm64, tagged with its version, its minor, its major and `latest`.

| Builder | Socket contract | AnsibleForms |
|---|---|---|
| 0.x | 3 | 7.8.0 and later |

## Documentation

The guides in [docs](docs) cover running it and what to check when it does not work.

| Guide | What it covers |
|---|---|
| [Install](docs/install.md) | Docker, Compose and Kubernetes, the settings, the security options |
| [Troubleshooting](docs/troubleshooting.md) | BuildKit, joining, registries over http, signing |

## Contributing

Contributions are welcome. Start with these files:

- [CONTRIBUTING.md](CONTRIBUTING.md): how to build and test it, the socket contract, and how it is released
- [CHANGELOG.md](CHANGELOG.md): the changes in every release
- [SECURITY.md](SECURITY.md): how to report a security issue

## License

[GPL-3.0](LICENSE).
