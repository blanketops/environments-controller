package bootstrap

import (
	"embed"
	_ "embed"
)

//go:embed *
var assets embed.FS
