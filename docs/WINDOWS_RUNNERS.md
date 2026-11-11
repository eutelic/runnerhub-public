# Windows native runners

RunnerHub can route trusted private-repository jobs to an enrolled Windows
amd64 process agent with the `runnerhub-windows` scale-set label. In
**Repositories → Custom runner profiles**, choose **Fill Windows MSVC profile**
and save it after the Windows host is online. The server always adds system
selectors for `os=windows` and `arch=amd64`; user tags cannot override them.

```yaml
jobs:
  build:
    runs-on: runnerhub-windows
    steps:
      - uses: actions/checkout@v4
      - name: Checkout AseApi
        uses: actions/checkout@v4
        with:
          repository: ArkServerApi/AseApi
          ref: main
          path: extern/AseApi
      - name: Configure
        shell: pwsh
        run: >
          cmake -S plugin -B build/plugin -A x64
          -DASE_API_DIR="$env:GITHUB_WORKSPACE/extern/AseApi"
      - name: Build
        shell: pwsh
        run: cmake --build build/plugin --config Release
      - name: Stage artifacts
        shell: pwsh
        run: |
          New-Item -ItemType Directory -Force runtime-plugin | Out-Null
          Copy-Item build/plugin/plugin/ArkCompat.dll runtime-plugin/
          Copy-Item build/plugin/plugin/PluginInfo.json runtime-plugin/
          if (Test-Path build/plugin/plugin/ArkCompat.pdb) {
            Copy-Item build/plugin/plugin/ArkCompat.pdb runtime-plugin/
          }
      - uses: actions/upload-artifact@v4
        with:
          name: ArkCompat-tracer
          path: runtime-plugin/
          if-no-files-found: error
```

## Host requirements

- A dedicated Windows Server 2025 amd64 machine with the supported GitHub
  Actions runner distribution installed as an immutable template directory.
- Visual Studio 2025 Build Tools with the C++ workload and x64 MSVC toolset,
  CMake, PowerShell, and the Windows SDK required by the project.
- A Windows build of `runnerhub-agent`, configured with provider `process`,
  the template path in `RUNNERHUB_RUNNER_DIR`, and a persistent state directory.
  Enroll the agent as a dedicated, non-administrator Windows service account.
- Enough free disk for a fresh runner copy and build outputs. Keep Windows and
  Visual Studio servicing current and verify the runner template version before
  accepting jobs.

## Trust and licensing

This is native host execution with `host-persistent` isolation. Each job gets
a fresh copy of the runner template and a unique work directory, but Windows
does not provide a per-job VM boundary here. Workflow code runs as the agent
service account and can access resources available to that account. Use this
profile only for a private repository whose workflow authors are trusted; do
not use it for public repositories, fork pull requests, or workflows that
check out untrusted code. Do not run the agent service as an administrator or
place unrelated credentials in its account or machine scope.

RunnerHub does not install or license Visual Studio, MSVC, the Windows SDK, or
third-party build tools. The operator must provide each tool under its
applicable Microsoft and third-party license terms.

## Acceptance status

compilation of the agent is not proof of a working Windows host. Keep internal follow-up open
until a real Windows Server 2025 host completes the target repository's CMake
x64 build and PowerShell artifact staging, and the enrollment, cleanup, resource
limits, and recovery behavior have been verified there.
