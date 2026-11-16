# AnsibleForms RTE builder

[![CI](https://img.shields.io/github/actions/workflow/status/ansibleforms/builder/ci.yml?branch=main&label=CI)](https://github.com/ansibleforms/builder/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-GPL--3.0-blue)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-ansibleforms.com-informational)](https://ansibleforms.com)

The builder of the RTE images an [AnsibleForms](https://github.com/ansibleforms/ansibleforms) controller defines, written in Node.js.
It joins the controller, builds each image it is handed with BuildKit, and pushes it to its registry.

```bash
docker run -d --name af-builder \
  --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
  -e CONTROLLER_URL=https://forms.example.com -e RTE_TOKEN=<at least 16 characters> \
  -e RTE_JOIN_TOKEN=<a join token from the Designer's RTE builders> \
  -v af-builder:/home/node/work ghcr.io/ansibleforms/ansibleforms-rte-builder
```

## ✨ Features

- **Joins like a runner:** dials the controller, with a join token or an admin's approval
- **Builds with BuildKit:** a rootless buildkitd of its own, or the one `BUILDKIT_HOST` names
- **Pushes to any registry:** the login comes sealed with each build, never stored
- **Signs and lists:** cosign signatures, offline, and an SBOM pushed with the image
- **Live log:** every line of a build reaches the controller's job, masked of its secrets
- **Survives a drop:** what the controller did not store is sent again after a reconnect
- **Knows nothing it does not need:** no database, no repositories, no playbooks

## 📚 Documentation

| Document | What it covers |
|---|---|
| [Install](docs/install.md) | Docker, Compose and Kubernetes, the settings, the security options |
| [Troubleshooting](docs/troubleshooting.md) | BuildKit, registries over http, signing, joining |
| [Contributing](CONTRIBUTING.md) | building, testing, the socket contract, releasing |

## 🔌 How it works with the controller

The controller does the thinking: the RTE images are defined in its Designer, and it writes each one's Dockerfile.
The builder only builds: it is handed the Dockerfile and its files, the reference to push to,
and the registry login and signing key sealed for it, then streams the build back as a job.

The two meet over the controller's runner socket, at a version of its **contract**.
A builder works with every controller that speaks the same contract, whatever their releases.

## 🤝 Contributing

Pull requests are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md). Security problems: see [SECURITY.md](SECURITY.md).

## 👥 Authors

- Blai Peidro ([github.com/blaipr](https://github.com/blaipr))

## ⚖️ License

[GPL-3.0](LICENSE), as AnsibleForms.
