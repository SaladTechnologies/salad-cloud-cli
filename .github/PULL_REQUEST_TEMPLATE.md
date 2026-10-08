<!--

Thanks for opening a pull request! Here are some tips:

- Review the contributing guidelines and code of conduct, and discuss larger changes with the team first.
- Exit codes, JSON output shapes, and error kinds are public contracts (see AGENTS.md). Call out any change to them.

-->

## Checklist

- [ ] `go vet ./...` and `go test ./...` pass, and `gofmt -l .` prints nothing
- [ ] `docs/` (and the README, if install or getting started changed) and `CHANGELOG.md` (under Unreleased) reflect the change
- [ ] Anything that touches an API call was exercised live, or this pull request says it was not
