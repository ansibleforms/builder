# Contributing to the AnsibleForms RTE Builder

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

The builder is one small Go program; each package under `internal/` holds one part of it.

| Path | Holds |
|---|---|
| `main.go` | the start: its settings, its BuildKit, its probes, its dial; the `health` and `version` commands |
| `internal/builder/` | a build: its files, the build and push, the signature, the items it sends |
| `internal/sign/` | signs an image with a cosign key, as cosign 3 does, without the cosign binary |
| `internal/buildkit/` | its own rootless buildkitd, and builds through BuildKit's Go client |
| `internal/registry/` | the registry login a build pushes with |
| `internal/settings/` | its environment variables |
| `internal/socket/` | the controller's socket, as the controller speaks it: protocol, session, spool, dial |
| `internal/version/` | its version, which follows AnsibleForms' |

---

## Building and testing

The builder needs the Go version `go.mod` names; the code is formatted with gofmt and checked with go vet.
Every source file has its own `_test.go`, and the tests cover every statement: CI fails below 100.0%.

```bash
gofmt -l .
go vet ./...
go test -race ./...
go test -coverpkg=./... -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1
docker build -t ansibleforms-rte-builder:dev .
```

`TestRealBuild` builds and pushes a real image when `E2E_BUILDKIT_HOST` names a BuildKit and `E2E_REGISTRY` a plain http registry;
with `E2E_COSIGN`, a cosign binary, it also signs the image and checks the signature with `cosign verify`, as CI does.

To try it against a controller, run the image as the README shows, with a join token made in the Designer's RTE builders.

---

## The socket contract

The builder speaks the controller's runner socket, at the contract `internal/socket/protocol.go` sets (`SocketContract`).
The package follows the controller's `server/src/rte/socket` files and changes only when its contract does.

When the controller raises its contract, release a builder that speaks it: port what changed in those files, raise
`SocketContract`, and release. The weekly Contract workflow compares it with the controller's latest release and fails until then.

---

## Pull requests

Pull requests are squash-merged, and the PR title becomes the commit, so it is a Conventional Commit with a lower-case subject.
Branches are named `<type>/<description>`, and one pull request holds one change.

---

## Releasing

The builder's version follows AnsibleForms'. When the app releases, it tells this repository, and Follow app release
opens a pull request that moves `internal/version/version.go` to that version and turns the changelog's Unreleased section into its entry.
Once it is merged and CI is green, the Release workflow publishes `ghcr.io/ansibleforms/ansibleforms-rte-builder` on amd64 and arm64 and creates the release.
Changes merged between two app releases are listed under Unreleased and go out with the next one.
