# RunnerHub alpha quickstart

RunnerHub runs ephemeral GitHub Actions workers on compute you already own.

The recommended setup is now:

```text
deploy RunnerHub
      ↓
Sign in with GitHub
      ↓
Install / manage RunnerHub GitHub App
      ↓
select repositories
      ↓
Connect
      ↓
runs-on: runnerhub
```

Repository installation IDs and repository URLs no longer need to be copied into the normal image-based deployment configuration.

## 1. Create the GitHub App

Create one GitHub App.

Repository permissions:

```text
Administration: Read & write
Metadata: Read-only
```

Disable webhooks.

Set the App's:

```text
Homepage URL    <RUNNERHUB_PUBLIC_URL>
Callback URL    <RUNNERHUB_PUBLIC_URL>/auth/github/callback
Setup URL       <RUNNERHUB_PUBLIC_URL>/auth/github/setup
```

Enable redirect after installation/update for the Setup URL.

Generate:

- a client secret;
- an RSA private key.

Record:

- client ID;
- client secret;
- App slug;
- private key.

The App does not need to be installed on a repository before RunnerHub itself is deployed.

## 2. Deploy RunnerHub

For TrueNAS SCALE, follow [the TrueNAS guide](../deploy/truenas/README.md).

For image-based Docker, configure:

```dotenv
RUNNERHUB_GITHUB_APP_ISSUER=Iv1.your-client-id
RUNNERHUB_GITHUB_APP_SLUG=your-app-slug
RUNNERHUB_PUBLIC_URL=http://runnerhub-host:8080
RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE=/absolute/path/github-app.pem
RUNNERHUB_GITHUB_CLIENT_SECRET_FILE=/absolute/path/github-client-secret
```

The release Compose stack starts:

- control plane;
- Docker host agent;
- dynamic scale-set manager.

The manager can be healthy with zero connected repositories.

## 3. Sign in

Open `RUNNERHUB_PUBLIC_URL`.

Click:

```text
Sign in with GitHub
```

The first GitHub identity that signs in becomes the local RunnerHub owner. That owner claim persists on disk. Browser sessions are intentionally memory-only and require signing in again after a control-plane restart.

API keys remain available for automation and recovery.

## 4. Install/select repositories

Click:

```text
Install / manage repositories
```

GitHub lets you install the RunnerHub App on an account/organization and choose all or selected repositories.

GitHub redirects back to RunnerHub's Setup URL. RunnerHub verifies that the returned installation is accessible to the signed-in user, then queries the repositories exposed by that installation.

## 5. Connect a repository

The dashboard lists repositories RunnerHub can see.

Click **Connect** on a private repository.

RunnerHub:

1. validates that the repository is available through the selected GitHub App installation;
2. creates a repository-scoped RunnerHub pool;
3. associates the GitHub installation ID with the pool;
4. exposes the pool to the dynamic scale-set manager;
5. starts an outbound listener for that repository.

No Compose edit or service restart is required.

Public repositories remain denied by default because the Docker trust model is designed for trusted private CI.

## 6. Run a workflow

In the connected repository:

```yaml
name: RunnerHub smoke

on:
  workflow_dispatch:

jobs:
  smoke:
    runs-on: runnerhub
    steps:
      - uses: actions/checkout@v6
      - run: |
          uname -a
          echo "RunnerHub smoke test passed"
```

Dispatch it.

Expected lifecycle:

1. GitHub sends job demand through the outbound scale-set message session.
2. RunnerHub selects a host.
3. RunnerHub obtains short-lived JIT runner configuration.
4. The host creates an ephemeral worker.
5. The job runs.
6. RunnerHub removes the worker and runner registration.

## 7. Docker compatibility

Default isolation:

```text
container-trusted
```

This intentionally does not provide a Docker daemon to workflow jobs.

Trusted workflows requiring Docker container actions, service containers, or `container:` jobs can use:

```text
container-dind
```

That mode creates a private privileged Docker-in-Docker daemon for each worker. It does not mount the host Docker socket into the job container, but it is still trusted-workload-only.

## Legacy/manual configuration

The source-development Compose path retains the existing static environment configuration for tests and manual deployments:

```dotenv
RUNNERHUB_GITHUB_APP_ISSUER=Iv1...
RUNNERHUB_GITHUB_APP_INSTALLATION_ID=12345678
RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE=/path/app.pem
RUNNERHUB_GITHUB_URL=https://github.com/OWNER/REPOSITORY
```

That path remains supported as a fallback, but browser onboarding is the recommended image-based deployment experience.
