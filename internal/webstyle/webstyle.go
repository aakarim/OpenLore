package webstyle

import _ "embed"

//go:embed openlore.css
var CSS []byte

//go:embed outfit.woff2
var Outfit []byte

const Link = `<link rel="stylesheet" href="/assets/openlore/app.css">`
