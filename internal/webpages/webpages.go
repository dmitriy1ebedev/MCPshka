// Package webpages holds the owner web pages as plain HTML files. Pages
// render data with textContent only; the server injects the CSP nonce into
// the single <script> tag.
package webpages

import _ "embed"

//go:embed issues.html
var Issues string

//go:embed memory.html
var Memory string
