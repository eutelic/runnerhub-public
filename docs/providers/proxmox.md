# Proxmox provider

The Proxmox backend clones an allowlisted template VM, writes a per-worker cloud-init snippet to a configured snippets directory, configures CPU/memory and `cicustom`, starts the VM, and later stops/deletes it by exact VM ID.

The template must contain the GitHub Actions runner in `/home/runner/actions-runner`. The snippet contains only the short-lived JIT configuration. This provider requires acceptance testing against the target Proxmox version/storage layout before production use.
