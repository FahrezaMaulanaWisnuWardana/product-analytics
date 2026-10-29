package web

import "embed"

//go:embed index.html app.js style.css sdk.js
var Files embed.FS
