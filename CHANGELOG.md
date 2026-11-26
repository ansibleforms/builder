# Changelog

Each release of the RTE builder has the version of the AnsibleForms release it follows, and is published with it.
The changes made to the builder between two releases wait under Unreleased and go out with the next one.

## Unreleased

### Changed

- **Written in Go:** one binary that also signs, on Alpine; the image is 235 MB instead of 794 MB
- **Healthcheck:** the image checks itself with `ansibleforms-rte-builder health`; buildctl and cosign are no longer in it

## 7.7.0

The RTE builder of [AnsibleForms 7.7.0](https://github.com/ansibleforms/ansibleforms/releases/tag/7.7.0).

## 7.6.0

The first release of the RTE builder, published with the version of the latest AnsibleForms release.
It needs a controller with RTE images (the Designer's RTE images and builders, AnsibleForms 7.8.0 and later):
a 7.6.0 controller does not know builders.

### Added

- **Builds the RTE images a controller defines:** joins it, builds each image with BuildKit, pushes it to its registry
- **Signs and lists:** cosign signatures, offline, and an SBOM pushed with the image
- **Live log:** every line of a build reaches the controller's job, its secrets masked
