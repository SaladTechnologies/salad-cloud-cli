# Security

## Reporting Security Vulnerabilities

We take security seriously, and we appreciate your cooperation in disclosing vulnerabilities to us responsibly.

_Please do not use public GitHub issues to report a security vulnerability._

Instead, please do one of the following:

- Open a GitHub security advisory on the
  [GitHub repository](https://github.com/SaladTechnologies/salad-cloud-cli/security/advisories/new)
- Send an email to [`dev@salad.com`](mailto:dev@salad.com)

Please include as much information as you can provide to help us better understand the nature and scope of the issue.

The CLI stores your API key in its config file (`saladcloud config path` prints the location) and never prints, logs,
or writes it anywhere else. A case where a credential leaks into output, logs, or another file is a security issue
and should be reported this way.
