# Contributing to the AnsibleForms RTE builder

Thanks for helping out. This repository is the builder of the RTE images an AnsibleForms controller defines.
The application itself lives in [ansibleforms/ansibleforms](https://github.com/ansibleforms/ansibleforms).

## Table of contents

- [What lives where](#what-lives-where)
- [Building and testing](#building-and-testing)
- [The socket contract](#the-socket-contract)
- [Pull requests](#pull-requests)
- [Releasing](#releasing)

---

## What lives where

The builder is one small Node.js program; each file under `src/` holds one part of it.

| Path | Holds |
|---|---|
| `index.js` | the start: its settings, its BuildKit, its probes, its dial |
| `src/builder.js` | a build: its files, buildctl, the push, cosign, the items it sends |
| `src/buildkit.js` | its own rootless buildkitd, unless `BUILDKIT_HOST` names one |
| `src/registry.js` | the registry login a build pushes with |
| `src/settings.js` | its environment variables |
| `src/socket/` | the controller's socket, copied from it: protocol, session, spool, dial |

---

## Building and testing

The builder needs Node.js 24 or newer; the tests run with vitest.

```bash
npm ci
npm test
docker build -t ansibleforms-rte-builder:dev .
```

To try it against a controller, run the image as the README shows, with a join token made in the Designer's RTE builders.

---

## The socket contract

The builder speaks the controller's runner socket, at the contract `src/socket/protocol.js` sets (`SOCKET_CONTRACT`).
The files in `src/socket/` are copies of the controller's and change only when its contract does.

When the controller raises its contract, release a builder that speaks it: copy the changed files, raise
`SOCKET_CONTRACT`, and release. The weekly Contract workflow compares it with the controller's latest release and fails until then.

---

## Pull requests

Pull requests are squash-merged, and the PR title becomes the commit, so it is a Conventional Commit with a lower-case subject.
Branches are named `<type>/<description>`, and one pull request holds one change.

---

## Releasing

The builder's version follows AnsibleForms'. When the app releases, it tells this repository, and Follow app release
opens a pull request that moves `package.json` to that version and turns the changelog's Unreleased section into its entry.
Once it is merged and CI is green, the Release workflow publishes `ghcr.io/ansibleforms/ansibleforms-rte-builder` on amd64 and arm64 and creates the release.
Changes merged between two app releases are listed under Unreleased and go out with the next one.
