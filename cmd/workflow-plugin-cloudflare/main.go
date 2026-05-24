// Command workflow-plugin-cloudflare is a workflow IaC plugin that implements
// the `infra.dns` resource type against Cloudflare DNS.
package main

import (
	"github.com/GoCodeAlone/workflow-plugin-cloudflare/internal"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

func main() {
	sdk.ServeIaCPlugin(internal.NewIaCServer(), sdk.IaCServeOptions{
		BuildVersion: sdk.ResolveBuildVersion(internal.Version),
	})
}
