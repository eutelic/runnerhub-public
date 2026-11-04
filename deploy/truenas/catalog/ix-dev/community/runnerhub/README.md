# RunnerHub

RunnerHub is a self-hosted control plane for running GitHub Actions jobs on
compute you own.

The TrueNAS Community app runs the RunnerHub control plane, Docker-backed host
agent, and optional GitHub scale-set listener. A fresh install is expected to
become healthy before any repository is connected.

**Before installing**

Create a GitHub App for RunnerHub and have these values ready:

Have the numeric GitHub App ID, client ID, slug, client secret, private key,
and the public URL you will use to open RunnerHub ready.

The client secret and private key are private install-form fields. RunnerHub's
one-shot bootstrap service writes them into the app's private persistent secret
storage; the long-running services consume the materialized secret files.

**Docker access and security boundary**

RunnerHub's trusted agent mounts /var/run/docker.sock so it can create,
inspect, and remove ephemeral GitHub Actions worker containers.

**Access to the Docker socket is root-equivalent access to the TrueNAS host.**
Install RunnerHub only if you intend to grant the application that authority.

The host Docker socket is not mounted into GitHub Actions workflow containers.
The catalog package also forces privileged-worker support off, so the optional
Docker-in-Docker publisher mode available in advanced/custom RunnerHub
deployments is not part of the Community catalog edition.

**TrueNAS-specific restrictions**

The Community catalog package is deliberately narrower than RunnerHub's custom
TrueNAS deployment:

The provider is fixed to Docker, without TrueNAS middleware or midclt
access, self-redeployment integration, or privileged/DinD workers. Storage,
port, and resource configuration use standard TrueNAS app template helpers.

These restrictions keep the catalog package independent of the TrueNAS
management API while preserving normal trusted-container GitHub Actions jobs.

**After installing**

Open the RunnerHub portal and sign in with GitHub. Install or select the
RunnerHub GitHub App installation, choose repositories to manage, and connect
a repository. To run a workflow, set its runs-on label to runnerhub.

No repository connection is required for the app itself to remain healthy.
