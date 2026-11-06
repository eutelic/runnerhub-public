# TrueNAS catalog staging

This directory mirrors the files that will be submitted under
`ix-dev/community/runnerhub/` in the upstream `truenas/apps` repository.

The catalog package deliberately differs from RunnerHub's power-user Custom App
deployment:

- the agent provider is fixed to `docker`;
- the TrueNAS `midclt` provider and middleware API access are not used;
- privileged worker/DinD mode is not exposed;
- GitHub credentials are collected as masked form values and materialized by the
  short-lived bootstrap container into the app's private secrets storage;
- RunnerHub's agent is the only long-running container with the host Docker
  socket;
- runtime images use a versioned RunnerHub release tag rather than `latest`.

Before upstream submission:

1. Publish the RunnerHub source repository and select an open-source license.
2. Publish a signed/annotated RunnerHub release (the initial overlay targets
   `v0.1.0`) and make all referenced GHCR images anonymously pullable.
3. Replace every RunnerHub image tag in `ix_values.yaml` with the release tag
   plus its immutable `@sha256:...` digest.
4. Copy `ix-dev/community/runnerhub` into a fork of `truenas/apps`.
5. Run the upstream app CI/copy-lib tooling so `templates/library`, generated
   metadata, and the current library hash are refreshed.
6. Run the upstream port validator. The staging default is 30559 as of
   November 6, 2026; upstream assigned 30557 to `community/cbzforge`.
   Recheck upstream before submission because reserved ports can change. This
   changes only the catalog's **default for new installations** and does not
   change the port of an existing TrueNAS Custom App deployment.
7. Add the final icon/screenshots through the TrueNAS media workflow.
8. Open the required TrueNAS Apps issue, then the app-addition pull request.

Do not submit the catalog overlay while it references unpublished or private
artifacts.
