// Package assets holds the application icon art.
package assets

import "embed"

// Frames holds the icon at every rendered size: tray 16-32px and the
// header/About mark 32-96px depending on display scaling, plus the 256px
// master used for the window icon and as the fallback above the largest
// frame. A vector mark is not an option: the GL backend tessellates SVG
// without antialiasing, so it reads jagged on Windows. These frames are drawn
// 1:1 instead of minifying the master through the GPU's linear filter.
//
//go:embed appicon-*.png
var Frames embed.FS
