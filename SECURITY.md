# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest  | ✅        |

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Use [GitHub's private vulnerability reporting](https://github.com/ares-17/docker-awakening-gateway/security/advisories/new) to report a vulnerability confidentially.

Include as much detail as possible:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

You can expect a response within **7 days**.

## Security Considerations

docker-gateway requires access to the Docker socket (`/var/run/docker.sock`) to start and stop containers. This grants significant privileges — mount the socket **read-write** only if your deployment requires it, and restrict access to the gateway's admin endpoints (`/_status`, `/_metrics`) using the built-in authentication (`admin_auth` in `config.yaml`).
