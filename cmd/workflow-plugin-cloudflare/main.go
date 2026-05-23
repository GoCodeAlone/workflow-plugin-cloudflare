// Command workflow-plugin-cloudflare is a workflow IaC plugin that implements
// the `infra.dns` resource type against Cloudflare DNS.
package main

import (
	"github.com/GoCodeAlone/workflow-plugin-cloudflare/internal"
)

func main() {
	internal.ServeIaCPlugin(internal.NewIaCServer())
}
