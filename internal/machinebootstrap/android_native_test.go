package machinebootstrap

import ("strings";"testing";"os/exec";"runtime")

func TestNativeAndroidBootstrapDoesNotTouchStandardDockerAgent(t *testing.T) {
  req:=Request{HostName:"compute-android",ControlPlaneURL:"http://truenas:8787",AgentImage:"ghcr.io/eutelic/runnerhub-agent:latest",AndroidEmulator:true}
  start:=startAndroidNativeScript(req,"short-lived-token","/tmp/bootstrap-token")
  for _,needle:=range []string{"runnerhub-android-agent","useradd --system","/dev/kvm","sha256sum -c","2.338.2","runnerhub-agent","RUNNERHUB_PROVIDER=process","ProtectSystem=strict","docker_cmd cp","RUNNERHUB_ENROLLMENT_TOKEN_FILE"} {
    if !strings.Contains(start,needle){t.Errorf("native installer missing %q",needle)}
  }
  for _,forbidden:=range []string{"docker_cmd rm -f runnerhub-agent","--privileged","-v /var/run/docker.sock:/var/run/docker.sock"} {
    if strings.Contains(start,forbidden){t.Errorf("native installer must not modify normal agent via %q",forbidden)}
  }
  if !strings.Contains(finalizeAndroidNativeScript(req,"/tmp/bootstrap-token"),"state/enrollment-token"){t.Fatal("native bootstrap must remove enrollment token")}
  if strings.Contains(cleanupAndroidNativeScript(req,"/tmp/bootstrap-token"),"systemctl stop runnerhub-android-agent"){t.Fatal("failed retry must never stop a previously running service")}
}

func TestNativeAndroidInstallScriptParsesAsShell(t *testing.T) {
  if runtime.GOOS=="windows"{t.Skip("POSIX shell test")}
  path,err:=exec.LookPath("sh")
  if err!=nil{t.Skip("sh not installed")}
  req:=Request{HostName:"compute-android",ControlPlaneURL:"http://truenas:8787",
    AgentImage:"ghcr.io/eutelic/runnerhub-agent:latest",AndroidEmulator:true}
  for name,script:=range map[string]string{
    "start":startAndroidNativeScript(req,"enrollment-token","/tmp/runnerhub-token"),
    "finalize":finalizeAndroidNativeScript(req,"/tmp/runnerhub-token"),
    "cleanup":cleanupAndroidNativeScript(req,"/tmp/runnerhub-token"),
    "diagnostics":diagnosticsAndroidNativeScript(req),
  }{
    cmd:=exec.Command(path,"-n")
    cmd.Stdin=strings.NewReader(script)
    if output,err:=cmd.CombinedOutput();err!=nil{
      t.Fatalf("%s script has invalid POSIX shell syntax: %s: %v",name,output,err)
    }
  }
}

func TestNativeInstallerPullsBeforeInstallingCleanupTrap(t *testing.T) {
 script:=startAndroidNativeScript(Request{HostName:"compute-android",ControlPlaneURL:"https://runnerhub.example",AgentImage:"ghcr.io/eutelic/runnerhub-agent:latest"},"token","/tmp/token")
 pull:=strings.Index(script,"pull_agent_image\ntmp=")
 trap:=strings.Index(script,"trap cleanup_stage EXIT")
 if pull<0||trap<pull{t.Fatal("private-registry pulls may overwrite bootstrap cleanup trap")}
}

func TestNativeAndroidInstallerProvisionsRunnerRuntimeLibraries(t *testing.T) {
  req:=Request{HostName:"compute-android",ControlPlaneURL:"http://truenas:8787",
    AgentImage:"ghcr.io/eutelic/runnerhub-agent:latest",AndroidEmulator:true}
  script:=startAndroidNativeScript(req,"token","/tmp/token")
  for _,fragment:=range []string{"apt-get install -y --no-install-recommends", "curl tar sha256sum python3 unzip bash systemctl", "sudo_cmd bash \"$tmp/runtime-deps/$dep_path\""} {
    if !strings.Contains(script,fragment){t.Errorf("native bootstrap missing %q",fragment)}
  }
}

func TestNativeInstallerDoesNotRootExecuteUserMutableRunnerFiles(t *testing.T) {
  script:=startAndroidNativeScript(Request{HostName:"compute-android",ControlPlaneURL:"https://runnerhub.example",AgentImage:"ghcr.io/eutelic/runnerhub-agent:latest"},"token","/tmp/token")
  if strings.Contains(script,"sudo_cmd \"$RUNNER_DIR/bin/installdependencies.sh\""){
    t.Fatal("root execution must use a verified script in trusted temporary storage, not a mutable native-account file")
  }
  if !strings.Contains(script,"mkdir -m 0700 \"$tmp/runtime-deps\""){t.Fatal("dependency staging must use a private bootstrap directory")}
}

func TestNativeInstallerChecksPrivateHomeAsNativeAccount(t *testing.T) {
  script:=startAndroidNativeScript(Request{HostName:"compute-android",ControlPlaneURL:"https://runnerhub.example",AgentImage:"ghcr.io/eutelic/runnerhub-agent:latest"},"token","/tmp/token")
  for _,want:=range []string{
    "sudo_cmd -u \"$NATIVE_USER\" df -Pk \"$NATIVE_HOME\"",
    "sudo_cmd -u \"$NATIVE_USER\" cat \"$RUNNER_DIR/.runnerhub-runner-version\"",
  }{
    if !strings.Contains(script,want){t.Errorf("permission-safe native home check missing %q",want)}
  }
}

func TestNativeBootstrapRestrictsJITJobsToAndroidEmulator(t *testing.T){
 script:=startAndroidNativeScript(Request{HostName:"compute-android",ControlPlaneURL:"https://runnerhub.example",AgentImage:"ghcr.io/eutelic/runnerhub-agent:latest"},"token","/tmp/token")
 if !strings.Contains(script,"RUNNERHUB_ANDROID_EMULATOR_ONLY=true"){
    t.Fatal("dedicated native agent must reject ordinary native jobs")
 }
}

func TestNativeAndroidRunnerTemplateIsRootManaged(t *testing.T) {
 script:=startAndroidNativeScript(Request{HostName:"compute-android",ControlPlaneURL:"https://runnerhub.example",AgentImage:"ghcr.io/eutelic/runnerhub-agent:latest"},"token","/tmp/token")
 for _,want:=range []string{
   "RUNNER_DIR=/usr/local/share/runnerhub-android/actions-runner",
   "install -d -m 0755 -o root -g root \"$RUNNER_DIR\"",
   "chown -R root:root \"$RUNNER_DIR\"",
   "install -m 0644 -o root -g root \"$tmp/runner-version\"",
 }{
   if !strings.Contains(script,want){t.Errorf("native template missing protection %q",want)}
 }
 if strings.Contains(script,"chown -R \"$NATIVE_USER:$NATIVE_USER\" \"$RUNNER_DIR\""){
   t.Fatal("worker account must never own its trusted runner template")
 }
 if strings.Contains(script,"$NATIVE_HOME/actions-runner"){
   t.Fatal("runner template must not live in the mutable worker home")
 }
}
