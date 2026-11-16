# Troubleshooting the RTE builder

## Table of contents

- [BuildKit does not start](#buildkit-does-not-start)
- [It does not join](#it-does-not-join)
- [A push fails](#a-push-fails)
- [Signing fails](#signing-fails)

---

## BuildKit does not start

Its log says `buildkitd stopped` or `failed to setup UID/GID map`: the container may not make user namespaces.

- Run it with `--security-opt seccomp=unconfined --security-opt apparmor=unconfined`.
- Or run BuildKit apart and set `BUILDKIT_HOST` (see the install guide's Kubernetes section).

---

## It does not join

Its log says why each dial failed, once per cause.

- **401:** its `RTE_TOKEN` is not the one the controller keeps for it, or the join token is used up, expired or revoked.
- **409:** a runner (not a builder) already has its name: set another `RTE_NAME`.
- **Waiting for approval:** an admin approves it in the Designer's RTE builders.

---

## A push fails

The build's log, on the controller's Jobs page, shows BuildKit's own error.

- **unauthorized:** the registry credential's user or password (or token) is wrong.
- **http: server gave HTTP response to HTTPS client:** the registry is plain http: give its credential an `http://` address.

---

## Signing fails

cosign's error is in the build's log; the image is pushed already, unsigned.

- **decrypt:** the signing key credential's password is not the key's.
- The signature is pushed next to the image in the same registry: its user needs push rights there.
