package www

import "embed"

//go:embed *.html
//go:embed *.js
//go:embed *.css
//go:embed *.json
//go:embed *.png
//go:embed *.mjs
//go:embed *.wasm
var Static embed.FS
