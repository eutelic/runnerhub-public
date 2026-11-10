package deployer

import (
	"strings"
	"testing"
)

func TestIsRunnerHubDeployerCompose(t *testing.T) {
	tests := []struct {
		name    string
		compose string
		want    bool
	}{
		{"deployer image", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n", true},
		{"quoted image", "services:\n  deployer:\n    image: 'ghcr.io/eutelic/runnerhub-deployer:latest'\n", true},
		{"mismatched quotes", "services:\n  deployer:\n    image: 'ghcr.io/eutelic/runnerhub-deployer:latest\"\n", false},
		{"unclosed single quote", "services:\n  deployer:\n    image: 'ghcr.io/eutelic/runnerhub-deployer:latest\n", false},
		{"unclosed double quote", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\"\n", false},
		{"pinned image", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer@sha256:" + strings.Repeat("a", 64) + "\n", true},
		{"truncated digest", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer@sha256:abc\n", false},
		{"nonhex digest", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer@sha256:" + strings.Repeat("g", 64) + "\n", false},
		{"digest with suffix", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer@sha256:" + strings.Repeat("a", 64) + ":extra\n", false},
		{"unrelated custom app", "services:\n  web:\n    image: nginx:latest\n", false},
		{"similarly named image", "services:\n  web:\n    image: ghcr.io/eutelic/runnerhub-deployer-malicious:latest\n", false},
		{"unrelated service with deployer image", "services:\n  web:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n", false},
		{"commented deployer image", "services:\n  web:\n    # image: ghcr.io/eutelic/runnerhub-deployer:latest\n    image: nginx:latest\n", false},
		{"extension with deployer image", "x-example:\n  image: ghcr.io/eutelic/runnerhub-deployer:latest\nservices:\n  web:\n    image: nginx:latest\n", false},
		{"nested unrelated deployer image", "services:\n  web:\n    environment:\n      image: ghcr.io/eutelic/runnerhub-deployer:latest\n", false},
		{"duplicate deployer service", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n  deployer:\n    image: nginx:latest\n", false},
		{"duplicate image override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n    image: nginx:latest\n", false},
		{"tab indentation", "services:\n\tdeployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n", false},
		{"duplicate top-level services override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\nservices:\n  web:\n    image: nginx:latest\n", false},
		{"duplicate top-level services same deployer", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\nservices:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n", false},
		{"quoted top-level services override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n'services':\n  web:\n    image: nginx:latest\n", false},
		{"quoted deployer service override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n  'deployer':\n    image: nginx:latest\n", false},
		{"quoted image key override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n    'image': nginx:latest\n", false},
		{"second YAML document overrides trusted service", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n---\nservices:\n  web:\n    image: nginx:latest\n", false},
		{"YAML document terminator after trusted service", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n...\n", false},
		{"document marker before trusted service", "---\nservices:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n", false},
		{"whitespace top-level services override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\nservices :\n  web:\n    image: nginx:latest\n", false},
		{"whitespace deployer override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n  deployer :\n    image: nginx:latest\n", false},
		{"whitespace image override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n    image : nginx:latest\n", false},
		{"quoted services override with colon spacing", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n'services' :\n  web:\n    image: nginx:latest\n", false},
		{"quoted deployer override with colon spacing", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n  'deployer' :\n    image: nginx:latest\n", false},
		{"quoted image override with colon spacing", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n    'image' : nginx:latest\n", false},
		{"escaped quoted image key override", `services:
  deployer:
    image: ghcr.io/eutelic/runnerhub-deployer:latest
    "im\u0061ge": nginx:latest
`, false},
		{"escaped quoted deployer key override", `services:
  deployer:
    image: ghcr.io/eutelic/runnerhub-deployer:latest
  "depl\u006fyer":
    image: nginx:latest
`, false},
		{"escaped quoted services key override", `services:
  deployer:
    image: ghcr.io/eutelic/runnerhub-deployer:latest
"serv\u0069ces":
  web:
    image: nginx:latest
`, false},
		{"explicit image key override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n    ? image\n    : nginx:latest\n", false},
		{"explicit deployer key override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n  ? deployer\n  :\n    image: nginx:latest\n", false},
		{"explicit services key override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n? services\n:\n  web:\n    image: nginx:latest\n", false},
		{"inline services alias override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\nservices: *unrelated\n", false},
		{"inline services flow override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\nservices: {web: {image: nginx:latest}}\n", false},
		{"inline deployer alias override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n  deployer: *unrelated\n", false},
		{"YAML version directive", "%YAML 1.1\nservices:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n", false},
		{"YAML tag directive", "%TAG !custom! tag:example.com,2026:\nservices:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n", false},
		{"deployer merge mapping", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n    <<: *other\n", false},
		{"comment-suffixed services override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\nservices: # override\n  web:\n    image: nginx:latest\n", false},
		{"comment-suffixed deployer override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n  deployer: # override\n    image: nginx:latest\n", false},
		{"comment-suffixed image override", "services:\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n    image: # override\n      nginx:latest\n", false},
		{"missing compose", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRunnerHubDeployerCompose(tt.compose); got != tt.want {
				t.Fatalf("isRunnerHubDeployerCompose() = %v, want %v", got, tt.want)
			}
		})
	}
}
