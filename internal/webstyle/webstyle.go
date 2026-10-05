package webstyle

import _ "embed"

//go:embed openlore.css
var CSS []byte

// Familjen is the sans-serif the dashboard uses; the shared stylesheet loads
// it so server-rendered pages match.
//
//go:embed familjen-grotesk.woff2
var Familjen []byte

// Outfit is kept for the document browser's inline styles.
//
//go:embed outfit.woff2
var Outfit []byte

// Link loads the shared stylesheet and applies the theme the user picked in
// the dashboard (stored under the same localStorage key its toggle writes),
// falling back to the system preference through the stylesheet's media query.
const Link = `<link rel="stylesheet" href="/assets/openlore/app.css">
<script>try{var t=localStorage.getItem("openlore-theme");if(t==="light"||t==="dark")document.documentElement.dataset.theme=t}catch(e){}</script>`
