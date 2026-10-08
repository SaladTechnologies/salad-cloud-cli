# Contributing

Thanks for being here and for being awesome! 👍

The `saladcloud` CLI is in **beta**. Rough edges are expected, and reports of them are the most useful contribution
right now. The following sections outline the different ways to contribute to the project.

## Discussing

The easiest way to contribute to the project is by participating in
[GitHub discussions](https://github.com/SaladTechnologies/salad-cloud-cli/discussions). The community often chimes in with
helpful advice when you have a question, and you may also find yourself providing answers and helping others. Be sure to
review the [code of conduct](./CODE_OF_CONDUCT.md) before participating.

_Please do not use GitHub issues to ask a question._ We will politely close a GitHub issue that asks a question and
kindly refer you to one of the aforementioned avenues.

## Reporting Bugs

We're sorry if this happened to you! During the beta, please report anything that looks wrong: a crash, a wrong exit
code, a JSON document missing a key, output that contradicts `--help`, or a message that sent you the wrong way.

Create a [GitHub issue](https://github.com/SaladTechnologies/salad-cloud-cli/issues/new/choose) using the bug report
template. The most useful report includes:

- the output of `saladcloud version`
- the exact command you ran
- the error line, ideally re-run with `-o json` so it is the single-line `{"error":{...}}` document
- for setup or connectivity problems, the output of `saladcloud doctor`

**Never paste an API key.** The CLI never prints your API keys (`--debug=body` redacts them, and `webhook-secret get`
prints only the webhook secret, by design), but check anything you copy from your shell, environment, or config file
before posting it.

If the problem is with your SaladCloud account, billing, or a specific workload rather than the CLI itself, send an
[official support request](mailto:cloud@salad.com) instead.

## Requesting Features

We love a good idea. Do you have one? Consider jumping into
[GitHub discussions](https://github.com/SaladTechnologies/salad-cloud-cli/discussions) first. The community may have some
interesting insights.

You can create a [GitHub issue](https://github.com/SaladTechnologies/salad-cloud-cli/issues/new/choose) to request new
features.

## Reporting Security Vulnerabilities

We take security seriously, and we appreciate your cooperation in disclosing vulnerabilities to us responsibly. Refer to
our [security policy](./SECURITY.md) for more details.

_Please do not use public GitHub issues to report a security vulnerability._

## Changing Code

Interested in changing the world?

Before starting on any code contribution, please discuss it with the team first to ensure it fits the product's direction
and the CLI's public contracts. Exit codes, JSON output shapes, and error kinds are depended on by scripts and agents,
so changes to them are held to a high bar.

The development workflow, layout, and contracts are documented in [`AGENTS.md`](../AGENTS.md). In short, a change is done
when `go vet ./...`, `go test ./...` and `make lint` pass, and the docs and `CHANGELOG.md` reflect it.

Additionally, please consider taking a moment to read Miguel de Icaza's blog post titled
[Open Source Contribution Etiquette](https://tirania.org/blog/archive/2010/Dec-31.html) and Ilya Grigorik's blog post
titled [Don't "Push" Your Pull Requests](https://www.igvita.com/2011/12/19/dont-push-your-pull-requests/).
