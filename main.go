package main

import (
	"github.com/malloryai/steampipe-plugin-zoom/zoom"
	"github.com/turbot/steampipe-plugin-sdk/v5/plugin"
)

func main() {
	plugin.Serve(&plugin.ServeOpts{PluginFunc: zoom.Plugin})
}
