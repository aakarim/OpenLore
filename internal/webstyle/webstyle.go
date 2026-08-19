package webstyle

import _ "embed"

//go:embed openlore.css
var CSS []byte

const Link = `<link rel="stylesheet" href="/assets/openlore.css">`
