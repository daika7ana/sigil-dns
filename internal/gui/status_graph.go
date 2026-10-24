//go:build gui

package gui

import (
	"math"

	gogui "github.com/go-gui-org/go-gui/gui"
)

// statusGraph is the hero's illustration: a quiet shield with a state mark,
// drawn with DrawCanvas in the state's semantic ink.  It is purely geometric —
// no text, no live values — so the canvas stays stable and free of fake data.
func statusGraph(v statusVisual) gogui.View {
	return gogui.DrawCanvas(gogui.DrawCanvasCfg{
		ID:     "gui:status:graph",
		Width:  statusGraphWidth,
		Height: statusGraphHeight,
		Sizing: gogui.FixedFixed,
		A11YCfg: gogui.A11YCfg{
			A11YLabel: statusGraphLabel(v.kind),
		},
		// The canvas reads the state ink instead of a version.  A render only
		// happens after an invalidation, so this stays a static illustration.
		AlwaysRedraw: true,
		OnDraw: func(dc *gogui.DrawContext) {
			drawStatusGraph(dc, v)
		},
	})
}

// statusGraphLabel names the state the shield carries, so the illustration is
// not a decorative blank to assistive tech.
func statusGraphLabel(kind statusKind) string {
	switch kind {
	case kindRunning:
		return "Protection active"
	case kindDegraded, kindDirty:
		return "Protection needs attention"
	case kindUnverified:
		return "Protection status unverified"
	default:
		return "Protection off"
	}
}

// drawStatusGraph paints the halo, the ticked orbit, the shield, and its mark.
func drawStatusGraph(dc *gogui.DrawContext, v statusVisual) {
	w, h := statusGraphWidth, statusGraphHeight
	cx, cy := w/2, h/2
	ink := v.accent

	// Atmosphere: one soft halo and a single evenly ticked ring.  The ring is
	// symmetric — nothing radiates from the centre.
	dc.FilledCircleGradient(cx, cy, h*0.46, &gogui.CanvasGradient{
		Radial: true,
		Stops: []gogui.GradientStop{
			{Color: ink.WithOpacity(0.16), Pos: 0},
			{Color: ink.WithOpacity(0), Pos: 1},
		},
	})
	const ring = statusGraphHeight * 0.44
	dc.Circle(cx, cy, ring, ink.WithOpacity(0.16), 1)
	for i := 0; i < 8; i++ {
		a := float64(i) * math.Pi / 4
		dc.FilledCircle(
			cx+ring*float32(math.Cos(a)),
			cy+ring*float32(math.Sin(a)),
			1.6, ink.WithOpacity(0.38),
		)
	}

	const halfWidth, halfHeight = statusGraphWidth * 0.171, statusGraphHeight * 0.31
	shield := shieldPath(cx, cy, halfWidth, halfHeight)

	// A translucent body, so the hero's state gradient still shows through.
	dc.FillPathGradient(&shield, &gogui.CanvasGradient{
		X1: cx, Y1: cy - halfHeight, X2: cx, Y2: cy + halfHeight,
		Stops: []gogui.GradientStop{
			{Color: ink.WithOpacity(0.13), Pos: 0},
			{Color: ink.WithOpacity(0.03), Pos: 1},
		},
	}, gogui.FillNonzero)

	// A wide, faint pass under the crisp outline gives the edge a soft falloff.
	soft := gogui.StrokeStyle{Cap: gogui.StrokeRoundCap, Join: gogui.StrokeRoundJoin}
	dc.StrokePath(&shield, ink.WithOpacity(0.08), 5, soft)
	outline := ink.WithOpacity(0.85)
	if v.kind == kindStopped {
		outline = ink.WithOpacity(0.50)
	}
	dc.StrokePath(&shield, outline, 2, soft)

	// The mark changes with the state; a stopped session stays empty, so an
	// outline alone can never read as protection that is on.
	switch v.kind {
	case kindRunning:
		var check gogui.CanvasPath
		check.MoveTo(cx-halfWidth*0.34, cy+1)
		check.LineTo(cx-halfWidth*0.10, cy+halfHeight*0.21)
		check.LineTo(cx+halfWidth*0.39, cy-halfHeight*0.25)
		dc.StrokePath(&check, ink, 4.5, soft)
	case kindDegraded, kindDirty:
		var stem gogui.CanvasPath
		stem.MoveTo(cx, cy-halfHeight*0.27)
		stem.LineTo(cx, cy+halfHeight*0.10)
		dc.StrokePath(&stem, ink, 4.5, soft)
		dc.FilledCircle(cx, cy+halfHeight*0.27, 2.4, ink)
	case kindUnverified:
		var dash gogui.CanvasPath
		dash.MoveTo(cx-halfWidth*0.24, cy)
		dash.LineTo(cx+halfWidth*0.24, cy)
		dc.StrokePath(&dash, ink, 4.5, soft)
	}
}

// shieldPath builds the heraldic shield outline: rounded shoulders, straight
// sides, and a soft taper to the point.  Coordinates are canvas-local.
func shieldPath(cx, cy, hw, hh float32) gogui.CanvasPath {
	var p gogui.CanvasPath
	top := cy - hh
	const corner = 14

	p.MoveTo(cx-hw+corner, top)
	p.LineTo(cx+hw-corner, top)
	p.QuadTo(cx+hw, top, cx+hw, top+corner)
	p.LineTo(cx+hw, cy+hh*0.10)
	p.CubicTo(cx+hw, cy+hh*0.52, cx+hw*0.55, cy+hh*0.80, cx, cy+hh)
	p.CubicTo(cx-hw*0.55, cy+hh*0.80, cx-hw, cy+hh*0.52, cx-hw, cy+hh*0.10)
	p.LineTo(cx-hw, top+corner)
	p.QuadTo(cx-hw, top, cx-hw+corner, top)
	p.Close()

	return p
}
