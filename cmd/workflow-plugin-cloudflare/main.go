// Command workflow-plugin-cloudflare is a workflow IaC plugin that implements
// the `infra.dns` resource type against Cloudflare DNS.
package main

import (
	_ "embed"

	"github.com/GoCodeAlone/workflow-plugin-cloudflare/internal"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

// pluginJSON is the canonical manifest embedded at build time so the binary's
// runtime ManifestProvider matches what tooling (wfctl plugin
// verify-capabilities) sees in plugin.json on disk. Without this embed the
// binary's Manifest.Name comes back empty and the release-pipeline truth-check
// rejects the artifact (root cause of the v0.1.0 release-publish failure).
//
//go:embed plugin.json
var pluginJSON []byte

func main() {
	sdk.ServeIaCPlugin(internal.NewIaCServer(), sdk.IaCServeOptions{
		ManifestProvider: sdk.MustEmbedManifest(pluginJSON),
		BuildVersion:     sdk.ResolveBuildVersion(internal.Version),
	})
}
