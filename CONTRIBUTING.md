# Contributing to Stowcloud

Stowcloud is maintained as a personal project.

## Local development

Use Linux for the server, or run it inside WSL2 or a Linux container. Install Go, pnpm, Python 3, and `just`, then install frontend dependencies with `cd frontend && pnpm install --frozen-lockfile`.

From the repository root, run `just dev` to start local development, `just build` to build the embedded server, `just test` for unit and type checks, `just e2e` for browser checks against a real server, and `just lint` for static checks. `just clean` removes generated binaries and Python cache files while keeping test artifacts.

For frontend hot reload, keep `just dev` running and start `cd frontend && pnpm dev`. Vite serves the app over https, on port 5173 when it is free, and proxies API calls to that server.

## Pull requests

External pull requests are not accepted. Any pull request opened by someone other than the repository maintainer is closed automatically.

If you have an idea, a bug fix, or an improvement, please describe it in an issue rather than submitting code directly.

## Issues

Bug reports, feature suggestions, and questions are welcome through GitHub Issues.

When opening an issue:

- Use the matching issue form: Bug Report, Feature Request, or Question.
- Provide the requested reproduction steps and environment details.
- Check existing issues before submitting to avoid duplicates.

## Security

Do not report security vulnerabilities in public issues. Follow the instructions in [SECURITY.md](SECURITY.md) to submit reports privately through GitHub Security Advisories.
