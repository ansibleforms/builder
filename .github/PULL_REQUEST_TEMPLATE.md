<!--
The TITLE must be a Conventional Commit - `fix: ...`, `feat: ...` - because it becomes the
squash commit. See CONTRIBUTING.md.
-->

## What this changes

<!-- One or two sentences. If it follows an AnsibleForms change, link that pull request. -->

## Checklist

- [ ] Branch is named `<type>/<description>` and the title is a Conventional Commit
- [ ] `gofmt -l .` prints nothing, and `go vet ./...` and `go test ./...` pass
- [ ] Built the image and ran a build against a controller, or the change does not affect it
- [ ] A change of `internal/socket/` follows a change of the controller's socket contract
