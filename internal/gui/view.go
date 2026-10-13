//go:build gui

package gui

import (
	"fmt"
	"strings"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/daika7ana/dns-client/internal/ipc"
	"github.com/daika7ana/dns-client/internal/version"
)

// statusKind is the one state the window presents at a time.
type statusKind int

const (
	kindRunning statusKind = iota
	kindDegraded
	kindDirty
	kindStopped
	kindUnverified
)

// pageKind is the visible screen behind the left navigation.
type pageKind int

const (
	pageStatus pageKind = iota
	pageSettings
	pageRules
	pageAbout
)

// statusVisual is the resolved banner for a state: a headline, one calm
// sentence, and its theme colors.  Normal states stay quiet — only a genuine
// anomaly takes a warning or error tone.
type statusVisual struct {
	headline      string
	detail        string
	headlineStyle gogui.TextStyle
	bg            gogui.Color // banner fill
	accent        gogui.Color // the status dot
	kind          statusKind
}

// Layout constants for the shell.  The content column is capped so cards and
// fields never stretch into long empty bars on a wide window; the nav rail is
// an icon+label pane wide enough for the longest label.
const (
	// navRailWidth is the fixed width of one navigation item.  The rail adds
	// navRailPad on each side, so its total width is navRailWidth+2*navRailPad.
	navRailWidth float32 = 200
	// navRailPad is the inset of the rail's items from its own edges.
	navRailPad float32 = 8
	// contentMaxWidth caps the reading width of every page.
	contentMaxWidth float32 = 960
	// contentPad is the inset around the reading column and inside a page
	// column, trimmed a few px tighter than the PaddingMedium (14) rung on both
	// axes.
	contentPad float32 = 10
	// brandSize is the app mark's square edge.
	brandSize float32 = 34
	// navItemMinHeight is the navigation item's height floor, so the larger
	// glyph and its label sit comfortably and the hit target stays a good size.
	navItemMinHeight float32 = 40
	// headerMinHeight floors the header band to an even integer so its bottom
	// hairline lands on a whole pixel and reads the same weight as the nav-pane
	// divider (a fractional band would antialias it into two faint rows).
	headerMinHeight float32 = 64
)

// view builds the window for the current state.  It runs on the main thread
// during the frame pass.
func view(w *gogui.Window) gogui.View {
	app := gogui.State[App](w)
	t := gogui.CurrentTheme()
	windowWidth, _ := w.WindowSize()

	content := []gogui.View{
		header(t, app),
		divider(t),
		gogui.Row(gogui.ContainerCfg{
			ID:         "gui:body",
			Sizing:     gogui.FillFill,
			Padding:    gogui.PaddingNone,
			Spacing:    gogui.NoSpacing,
			SizeBorder: gogui.NoBorder,
			Content: []gogui.View{
				navRail(t, app),
				verticalDivider(t),
				contentArea(t, app, windowWidth),
			},
		}),
	}
	// The toast is a floating layer, not an in-flow card: it is lifted out of
	// the tree and painted bottom-right over the page.
	if app.toast != nil {
		content = append(content, toastOverlay(t, app))
	}

	return gogui.Column(gogui.ContainerCfg{
		Sizing:     gogui.FillFill,
		Color:      t.ColorBackground,
		Padding:    gogui.PaddingNone,
		Spacing:    gogui.NoSpacing,
		SizeBorder: gogui.NoBorder,
		Content:    content,
	})
}

// divider is the hairline under the header band.
func divider(t gogui.Theme) gogui.View {
	return gogui.Separator(gogui.SeparatorCfg{
		ID:    "gui:header:divider",
		Color: t.ColorBorder,
	})
}

// verticalDivider is the hairline between the nav rail and the page.
func verticalDivider(t gogui.Theme) gogui.View {
	return gogui.Separator(gogui.SeparatorCfg{
		ID:       "gui:nav:divider",
		Vertical: true,
		Color:    t.ColorBorder,
	})
}

// scrollbarGutter is the extra right inset on a page column.  The scrollbar is
// drawn at the column's own right edge, so this is the gap that lets the bar
// float in a gutter instead of abutting the cards.
const scrollbarGutter float32 = 6

// pagePadding is the inset inside a scrollable page column: contentPad on
// three sides, plus scrollbarGutter on the right.
func pagePadding() gogui.Padding {
	return gogui.NewPadding(contentPad, contentPad+scrollbarGutter, contentPad, contentPad)
}

// contentArea centers a capped reading column in the space beside the rail.
// HAlignCenter places the capped child box; MaxWidth on the child is what caps
// it, so the column stays readable at any window width.
func contentArea(t gogui.Theme, app *App, windowWidth int) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		ID:         "gui:contentwrap",
		Sizing:     gogui.FillFill,
		HAlign:     gogui.HAlignCenter,
		Padding:    gogui.NewPadding(contentPad, contentPad, contentPad, contentPad),
		Spacing:    gogui.NoSpacing,
		SizeBorder: gogui.NoBorder,
		Content:    []gogui.View{pageContent(t, app, windowWidth)},
	})
}

// pageContent is the body of the selected page.
func pageContent(t gogui.Theme, app *App, windowWidth int) gogui.View {
	switch app.page {
	case pageSettings:
		return settingsPage(t, app, windowWidth)
	case pageRules:
		return rulesPage(t, app)
	case pageAbout:
		return aboutPage(t)
	default:
		return statusPage(t, app)
	}
}

// navRail is the left NavigationView pane: one icon+label item per page.  The
// selected item takes an accent wash and a leading accent bar; the others stay
// ghost so they recede.  Like the header it takes the background tone, with the
// hairline divider doing the separation.
func navRail(t gogui.Theme, app *App) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		ID:         "gui:nav",
		Sizing:     gogui.FixedFit,
		Color:      t.ColorBackground,
		Padding:    gogui.NewPadding(t.PaddingSmall.Top, navRailPad, t.PaddingSmall.Bottom, navRailPad),
		Spacing:    gogui.SpacingTight,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			navItem(t, app, pageStatus, "Status", gogui.IconActivity),
			navItem(t, app, pageSettings, "Settings", gogui.IconGear),
			navItem(t, app, pageRules, "Rules", gogui.IconFilter),
			navItem(t, app, pageAbout, "About", gogui.IconInfo),
		},
	})
}

// navItem builds one navigation entry.  The item fills the rail's item width
// and carries a leading accent bar (transparent when unselected) so the labels
// sit on one column whatever the selection.
func navItem(t gogui.Theme, app *App, p pageKind, label, icon string) gogui.View {
	selected := p == app.page

	bar := gogui.ColorTransparent
	if selected {
		bar = t.ColorAccent
	}

	row := gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FillFill,
		Padding:    gogui.PaddingNone,
		Spacing:    gogui.SpacingMedium,
		SizeBorder: gogui.NoBorder,
		VAlign:     gogui.VAlignMiddle,
		Content: []gogui.View{
			// The selection indicator: a short accent bar at the item's left.
			gogui.Column(gogui.ContainerCfg{
				Width:      3,
				Height:     16,
				Sizing:     gogui.FixedFixed,
				Color:      bar,
				Radius:     gogui.RadiusPx(2),
				Padding:    gogui.PaddingNone,
				SizeBorder: gogui.NoBorder,
			}),
			navIcon(t, icon, selected),
			gogui.Text(gogui.TextCfg{
				Text:      label,
				TextStyle: t.TextStyleBody,
				Sizing:    gogui.FillFit,
			}),
		},
	})

	cfg := gogui.ButtonCfg{
		ID:        "gui:nav:" + pageName(p),
		Sizing:    gogui.FixedFit,
		Width:     navRailWidth,
		MinHeight: navItemMinHeight,
		Content:   []gogui.View{row},
		OnClick:   func(ctx gogui.EventCtx) { setPage(ctx, p) },
	}
	if selected {
		cfg.Variant = gogui.ButtonSecondary
		cfg.Colors = gogui.ColorSet{
			Base:   t.ColorAccentSubtle,
			Hover:  t.ColorAccentSubtle,
			Click:  t.ColorAccentSubtle,
			Focus:  t.ColorAccentSubtle,
			Border: gogui.ColorTransparent,
		}
	} else {
		cfg.Variant = gogui.ButtonGhost
	}

	return gogui.Button(cfg)
}

// navIcon is the item's leading glyph, in the accent when selected.  It sits on
// the IconLarge rung (17px at the tuned body 14) so it reads as a proper
// NavigationView icon beside the 14px label, not a small dot.
func navIcon(t gogui.Theme, icon string, selected bool) gogui.View {
	ts := t.TextStyleIconLarge
	ts.Color = t.TextStyleSecondary.Color
	if selected {
		ts.Color = t.ColorAccent
	}

	return gogui.Text(gogui.TextCfg{
		Text:      icon,
		TextStyle: ts,
		Sizing:    gogui.FitFit,
	})
}

// pageName is the stable suffix for a page's widget ID.
func pageName(p pageKind) (name string) {
	switch p {
	case pageSettings:
		return "settings"
	case pageRules:
		return "rules"
	case pageAbout:
		return "about"
	default:
		return "status"
	}
}

// About metadata.  Plain constants, so the page needs no service read or config
// file and renders the same in every state.
const (
	appName        = "dns-client"
	appAuthor      = "daika7ana"
	appAuthorURL   = "https://github.com/daika7ana"
	appRepoURL     = "https://github.com/daika7ana/dns-client"
	appLicense     = "GPLv3"
	appLicenseNote = "This program links AdguardTeam/urlfilter, which is licensed under GPL-3.0."
)

// aboutPage is the static read-only About screen: identity, version, author,
// repository, and license, in the same card/kv style as the Status page.  The
// author and repository values are links that open in the default browser.
func aboutPage(t gogui.Theme) gogui.View {
	rows := []gogui.View{
		kv(t, "Application", appName),
		kv(t, "Version", version.Version),
		kv(t, "IPC protocol", fmt.Sprintf("%d", version.ProtocolVersion)),
		kvLink(t, "gui:about:author", "Author", appAuthor, appAuthorURL),
		kvLink(t, "gui:about:repo", "Repository", appRepoURL, appRepoURL),
		kv(t, "License", appLicense),
		gogui.Text(gogui.TextCfg{
			ID:        "gui:about:license",
			Text:      appLicenseNote,
			TextStyle: t.TextStyleCaption,
			Sizing:    gogui.FillFit,
			Mode:      gogui.TextModeWrap,
		}),
	}

	return gogui.Column(gogui.ContainerCfg{
		ID:         "gui:about",
		Sizing:     gogui.FillFill,
		MaxWidth:   contentMaxWidth,
		Padding:    pagePadding(),
		Spacing:    gogui.SpacingMedium,
		SizeBorder: gogui.NoBorder,
		Scrollable: true,
		Content: []gogui.View{
			card(t, "gui:card:about", "About", rows),
		},
	})
}

// kvLink is a kv row whose value is a clickable link.  The label column matches
// kv so the About rows stay aligned; the value is link-styled accent text.
func kvLink(t gogui.Theme, id, label, text, url string) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		Spacing:    gogui.SpacingMedium,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			labelText(t, label),
			linkButton(t, id, text, url),
		},
	})
}

// linkButton is an inline link: a borderless ghost button around underlined
// accent text.  go-gui has no link widget, so the affordance is a ghost button
// (theme link text, pointer cursor and a hover fill) — clicking it, or pressing
// Space/Enter while focused, opens url in the default browser.
func linkButton(t gogui.Theme, id, text, url string) gogui.View {
	return gogui.Button(gogui.ButtonCfg{
		ID:      id,
		Variant: gogui.ButtonGhost,
		Padding: gogui.PaddingNone,
		Sizing:  gogui.FitFit,
		Content: []gogui.View{
			gogui.Text(gogui.TextCfg{
				Text:      text,
				TextStyle: t.TextStyleLink,
				Sizing:    gogui.FitFit,
			}),
		},
		OnClick: func(ctx gogui.EventCtx) { _ = openURL(url) },
	})
}

// statusPage is the status screen: a banner and the read-only card stack, all
// in ONE scrollable column.  Keeping the banner in the same column as the
// cards means they share the scroll region's inset, so the banner is exactly as
// wide as the cards below it.  The power control lives in the header.
func statusPage(t gogui.Theme, app *App) gogui.View {
	v := statusVisualFor(app.snap, t)

	content := []gogui.View{banner(t, v)}
	content = append(content, sections(app, t)...)

	return gogui.Column(gogui.ContainerCfg{
		ID:         "gui:content",
		Sizing:     gogui.FillFill,
		MaxWidth:   contentMaxWidth,
		Padding:    pagePadding(),
		Spacing:    gogui.SpacingMedium,
		SizeBorder: gogui.NoBorder,
		Scrollable: true,
		Content:    content,
	})
}

// header is the app's identity band: the app mark and title on the left, the
// window actions (theme, Restore, Protection) on the right.  It takes the
// background tone — chrome is background, cards are panel — so neutral buttons
// and the theme control read against it.
func header(t gogui.Theme, app *App) gogui.View {
	ps := powerStateFor(t, app)

	// The Protection button runs the existing enable/disable path.
	power := actionEnable
	if ps.on {
		power = actionDisable
	}

	caption := updatedText(app)
	switch {
	case app.busy:
		caption = app.busyNote
	case ps.note != "":
		caption = ps.note
	}

	controls := []gogui.View{
		gogui.SegmentedControl(gogui.SegmentedControlCfg{
			ID:      "gui:theme",
			Value:   app.themePref,
			Sizing:  gogui.FitFit,
			Options: themeOptions(),
			OnSelect: func(v string, ctx gogui.EventCtx) {
				setThemePref(ctx, v)
			},
		}),
	}
	if ps.restore {
		controls = append(controls, gogui.Button(gogui.ButtonCfg{
			ID:       "gui:action:restore",
			Variant:  gogui.ButtonPrimary,
			Label:    "Restore now",
			Disabled: app.busy,
			OnClick:  func(ctx gogui.EventCtx) { runAction(ctx, actionRestore) },
		}))
	}

	// The power control is the right-most action.  ONLY the enabled, actionable
	// Enable wears the success green; every other state — the protected Disable,
	// and an Enable that cannot run yet (not installed, unverified, busy) — is a
	// calm neutral secondary, so a disabled control reads inert instead of a
	// washed success block.  A healthy "protected" session is never danger-red.
	powerCfg := gogui.ButtonCfg{
		ID:       "gui:power",
		Variant:  gogui.ButtonSecondary,
		Label:    actionLabel(power),
		Disabled: !ps.canToggle,
		OnClick:  togglePower,
	}
	switch {
	case !ps.canToggle:
		// Inert: a neutral control fill, never a washed success block.
		powerCfg.Colors = disabledNeutralColors(t)
	case !ps.on:
		powerCfg.Variant = gogui.ButtonPrimary
		powerCfg.Colors = successButtonColors(t)
	}

	controls = append(controls, gogui.Button(powerCfg))

	return gogui.Row(gogui.ContainerCfg{
		ID:         "gui:header",
		Sizing:     gogui.FillFit,
		Color:      t.ColorBackground,
		MinHeight:  headerMinHeight,
		Padding:    gogui.NewPadding(t.PaddingMedium.Top, t.PaddingLarge.Left, t.PaddingMedium.Bottom, t.PaddingLarge.Right),
		Spacing:    gogui.SpacingMedium,
		VAlign:     gogui.VAlignMiddle,
		SizeBorder: gogui.NoBorder,
		Content:    append([]gogui.View{brand(t, caption)}, controls...),
	})
}

// brand is the app identity block: a mark, the title, and the status caption.
// It fills the header's free space so the actions land on the right.
func brand(t gogui.Theme, caption string) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		Padding:    gogui.PaddingNone,
		Spacing:    gogui.SpacingMedium,
		SizeBorder: gogui.NoBorder,
		VAlign:     gogui.VAlignMiddle,
		Content: []gogui.View{
			brandMark(t),
			gogui.Column(gogui.ContainerCfg{
				Sizing:     gogui.FitFit,
				Padding:    gogui.PaddingNone,
				Spacing:    gogui.SpacingTight,
				SizeBorder: gogui.NoBorder,
				Content: []gogui.View{
					gogui.Text(gogui.TextCfg{
						Text:      "dns-client",
						TextStyle: t.TextStyleTitle,
						Sizing:    gogui.FitFit,
					}),
					gogui.Text(gogui.TextCfg{
						Text:      caption,
						TextStyle: t.TextStyleCaption,
						Sizing:    gogui.FitFit,
						Mode:      gogui.TextModeWrap,
					}),
				},
			}),
		},
	})
}

// brandMark is the app's rounded accent square with the shield glyph.  The
// glyph sits on the IconMedium rung so it keeps proportion with the enlarged
// title beside it.
func brandMark(t gogui.Theme) gogui.View {
	ts := t.TextStyleIconMedium
	ts.Color = t.ColorTextOnAccent

	return gogui.Row(gogui.ContainerCfg{
		ID:         "gui:brand",
		Width:      brandSize,
		Height:     brandSize,
		Sizing:     gogui.FixedFixed,
		Color:      t.ColorAccent,
		Radius:     gogui.RadiusMedium,
		HAlign:     gogui.HAlignCenter,
		VAlign:     gogui.VAlignMiddle,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			gogui.Text(gogui.TextCfg{
				Text:      gogui.IconShield,
				TextStyle: ts,
				Sizing:    gogui.FitFit,
			}),
		},
	})
}

// powerStateFor resolves the single power control for the current snapshot.
func powerStateFor(t gogui.Theme, app *App) powerState {
	return planPower(statusVisualFor(app.snap, t).kind,
		app.snap.installed, app.snap.running, enabled(app.snap), app.busy, canEdit(app.snap))
}

// successButtonColors paints a button in the theme's success tone.  The toolkit
// has no success variant, so every slot is stated: a Primary button would pair
// the green fill with the accent border and hover.
func successButtonColors(t gogui.Theme) gogui.ColorSet {
	g := t.Cfg.ColorSuccess

	return gogui.ColorSet{Base: g, Hover: g, Click: g, Focus: g, Border: g}
}

// disabledNeutralColors is an inert neutral control look: a theme "active"
// surface fill with a neutral border, so a disabled button reads as a clearly
// shaped but dormant control.  Disabled is pinned so the renderer paints the
// muted fill as given rather than a dimmed base.
func disabledNeutralColors(t gogui.Theme) gogui.ColorSet {
	a := t.Cfg.ColorActive

	return gogui.ColorSet{Base: a, Hover: a, Click: a, Focus: a, Border: t.ColorBorder, Disabled: a}
}

// updatedText is the last-poll timestamp, folded into the header so it never
// occupies a footer band.
func updatedText(app *App) (s string) {
	if app.snap.updated.IsZero() {
		return "Not updated yet"
	}

	return "Updated " + app.snap.updated.Format("15:04:05")
}

// toast width and page-edge margin for the floating toast.
const (
	toastWidth  float32 = 320
	toastMargin float32 = 16
)

// toastOverlay is our own floating action-result toast.  We build it (rather
// than the toolkit toast) because the toolkit pins its close button to the
// corner with no padding and no ToastCfg lever; a Float container gives us a
// bottom-right overlay whose close button is padded and vertically centred on
// the title row.  It auto-dismisses (see showToast) and closes by hand.
func toastOverlay(t gogui.Theme, app *App) gogui.View {
	n := *app.toast
	title, bg := noticePalette(t, n.kind)

	return gogui.Column(gogui.ContainerCfg{
		ID:           "gui:toast",
		Sizing:       gogui.FixedFit,
		Width:        toastWidth,
		Float:        true,
		FloatAnchor:  gogui.FloatBottomRight,
		FloatTieOff:  gogui.FloatBottomRight,
		FloatOffsetX: -toastMargin,
		FloatOffsetY: -toastMargin,
		FloatZIndex:  1000,
		Color:        bg,
		ColorBorder:  t.ColorBorder,
		SizeBorder:   gogui.BorderThin,
		Radius:       gogui.RadiusLarge,
		Shadow:       t.Cfg.ShadowPopover,
		Padding:      t.PaddingMedium,
		Spacing:      gogui.SpacingTight,
		Content: []gogui.View{
			gogui.Row(gogui.ContainerCfg{
				ID:         "gui:toast:title",
				Sizing:     gogui.FillFit,
				Padding:    gogui.PaddingNone,
				SizeBorder: gogui.NoBorder,
				Spacing:    gogui.SpacingMedium,
				VAlign:     gogui.VAlignMiddle,
				Content: []gogui.View{
					gogui.Text(gogui.TextCfg{
						Text:      n.title,
						TextStyle: title,
						Sizing:    gogui.FillFit,
					}),
					gogui.Button(gogui.ButtonCfg{
						ID:      "gui:toast:dismiss",
						Variant: gogui.ButtonGhost,
						Label:   "\u00d7",
						OnClick: dismissToast,
					}),
				},
			}),
			gogui.Text(gogui.TextCfg{
				Text:      n.body,
				TextStyle: t.TextStyleSecondary,
				Mode:      gogui.TextModeWrap,
				Sizing:    gogui.FillFit,
			}),
		},
	})
}

// noticePalette maps a notice severity onto its title style and wash.  The
// theme's *Subtle colors are translucent (the toolkit builds them as the
// severity hue at alpha 30-40), so the wash is composited Over the opaque panel
// fill: a toast floats over live page content, so it must read as a solid card
// rather than a see-through sheet, while still carrying the severity tint.
func noticePalette(t gogui.Theme, k noticeKind) (title gogui.TextStyle, bg gogui.Color) {
	switch k {
	case noticeSuccess:
		return t.TextStyleSuccess, t.ColorSuccessSubtle.Over(t.ColorPanel)
	case noticeWarning:
		return t.TextStyleWarning, t.ColorWarningSubtle.Over(t.ColorPanel)
	case noticeError:
		return t.TextStyleError, t.ColorErrorSubtle.Over(t.ColorPanel)
	default:
		return t.TextStyleBody, t.ColorPanel
	}
}

// banner is the state surface: a rounded panel with a status dot, a headline,
// and one calm sentence.  The dot carries the state color; a healthy session
// keeps the neutral panel fill so the page never becomes a full-width wash,
// while a genuine anomaly tints the fill.
func banner(t gogui.Theme, v statusVisual) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		ID:          "gui:banner",
		Sizing:      gogui.FillFit,
		Color:       v.bg,
		ColorBorder: t.ColorBorder,
		SizeBorder:  gogui.BorderThin,
		Radius:      gogui.RadiusLarge,
		Padding:     t.PaddingLarge,
		Spacing:     gogui.SpacingSmall,
		Content: []gogui.View{
			gogui.Row(gogui.ContainerCfg{
				ID:         "gui:banner:title",
				Sizing:     gogui.FillFit,
				Padding:    gogui.PaddingNone,
				SizeBorder: gogui.NoBorder,
				Spacing:    gogui.SpacingMedium,
				VAlign:     gogui.VAlignMiddle,
				Content: []gogui.View{
					statusIndicator(v),
					gogui.Text(gogui.TextCfg{
						Text:      v.headline,
						TextStyle: v.headlineStyle,
						Sizing:    gogui.FillFit,
					}),
				},
			}),
			gogui.Text(gogui.TextCfg{
				Text:      v.detail,
				TextStyle: t.TextStyleSecondary,
				Mode:      gogui.TextModeWrap,
				Sizing:    gogui.FillFit,
			}),
		},
	})
}

// statusIndicator is the state dot: a ring in the state colour, filled with a
// soft glow when protection is on, a hollow ring otherwise.
func statusIndicator(v statusVisual) gogui.View {
	fill := gogui.ColorTransparent
	var glow *gogui.BoxShadow
	if v.kind == kindRunning {
		fill = v.accent
		glow = &gogui.BoxShadow{Color: v.accent, BlurRadius: 6, Spread: 1}
	}

	return gogui.Circle(gogui.ContainerCfg{
		ID:          "gui:banner:dot",
		Width:       12,
		Height:      12,
		Sizing:      gogui.FixedFixed,
		Color:       fill,
		ColorBorder: v.accent,
		SizeBorder:  gogui.BorderPx(2),
		Padding:     gogui.PaddingNone,
		Shadow:      glow,
	})
}

// togglePower flips protection through the existing enable/disable actions:
// turning on runs Enable (which starts the service and applies interception),
// turning off runs Disable (which removes interception; the service stays
// installed).  The header button calls it.
func togglePower(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)
	if enabled(app.snap) {
		runAction(ctx, actionDisable)
	} else {
		runAction(ctx, actionEnable)
	}
}

// sections is the Status page's read-only card stack, built from the effective
// config summary (the live one, or the cached last-good one while the service
// is stopped).  An unverified source shows no config data at all.
func sections(app *App, t gogui.Theme) []gogui.View {
	if !canEdit(app.snap) {
		return nil
	}

	sum := app.summary()

	return []gogui.View{
		profileSection(sum, t),
		filteringSection(sum, t),
		cacheSection(sum, t),
	}
}

// profileSection renders the active profile's display-only labels and a masked
// resolver id (SPEC §15.2: never the id itself).
func profileSection(sum *ipc.ConfigSummary, t gogui.Theme) gogui.View {
	rows := []gogui.View{}
	if sum == nil {
		rows = append(rows, kv(t, "Profile", "—"))
	} else {
		rows = append(rows,
			kv(t, "Profile", fallback(sum.Profile, "—")),
			kv(t, "Provider", fallback(sum.Provider, "—")),
			kv(t, "Variant", fallback(sum.Variant, "—")),
			kv(t, "Protocol", fallback(sum.Protocol, "—")),
			kv(t, "Resolver ID", resolverID(sum.IDSet)),
		)
	}

	return card(t, "gui:card:profile", "Profile", rows)
}

// filteringSection renders filtering settings, list hosts, and rule counts.
func filteringSection(sum *ipc.ConfigSummary, t gogui.Theme) gogui.View {
	rows := []gogui.View{}
	if sum == nil {
		rows = append(rows, kv(t, "Status", "—"))
	} else {
		rows = append(rows,
			kv(t, "Filtering", yesNo(sum.FilteringEnabled)),
			kv(t, "Block response", fallback(sum.BlockResponse, "—")),
			kv(t, "Rules (per-domain)", fmt.Sprintf("%d", sum.PerDomainCount)),
			kv(t, "Filter lists", fmt.Sprintf("%d", sum.ListCount)),
		)
		for _, l := range sum.Lists {
			host := fallback(l.Host, "(list)")
			rows = append(rows, kv(t, "• List", host+"  ("+enabledWord(l.Enabled)+")"))
		}
	}

	return card(t, "gui:card:filtering", "Filtering", rows)
}

// cacheSection renders cache and bootstrap settings.
func cacheSection(sum *ipc.ConfigSummary, t gogui.Theme) gogui.View {
	rows := []gogui.View{}
	if sum == nil {
		rows = append(rows, kv(t, "Status", "—"))
	} else {
		rows = append(rows,
			kv(t, "Cache", yesNo(sum.CacheEnabled)),
			kv(t, "Cache size", humanBytes(sum.CacheSizeBytes)),
			kv(t, "Bootstrap", fallback(strings.Join(sum.Bootstrap, ", "), "—")),
		)
	}

	return card(t, "gui:card:cache", "Cache & bootstrap", rows)
}

// card is a Fluent surface: a rounded panel with a hairline border and a
// heading.  Cards separate from the page by fill value, never by a shadow.  It
// uses a plain filled/outlined Column with an in-flow heading Text rather than
// the toolkit's group-box Title: the group-box legend is drawn outside the
// scrollable clip, so scrolled cards painted their legend at the scroll origin
// and overlapped the banner, and its TitleBG struck a line through the text.
func card(t gogui.Theme, id, title string, rows []gogui.View) gogui.View {
	return cardSpaced(t, id, title, gogui.SpacingMedium, rows)
}

// cardSpaced is card with an explicit inter-row gap.  The medium default gives
// every card the same vertical rhythm; a caller can tighten it for a dense
// read-only list.
func cardSpaced(t gogui.Theme, id, title string, spacing gogui.Spacing, rows []gogui.View) gogui.View {
	content := make([]gogui.View, 0, len(rows)+1)
	content = append(content, cardTitle(t, title))
	content = append(content, rows...)

	return gogui.Column(gogui.ContainerCfg{
		ID:          id,
		Sizing:      gogui.FillFit,
		Color:       t.ColorPanel,
		ColorBorder: t.ColorBorder,
		SizeBorder:  gogui.BorderThin,
		Radius:      gogui.RadiusLarge,
		Padding:     t.PaddingLarge,
		Spacing:     spacing,
		Content:     content,
	})
}

// cardTitle is a card's heading line.
func cardTitle(t gogui.Theme, title string) gogui.View {
	return gogui.Text(gogui.TextCfg{
		Text:      title,
		TextStyle: t.TextStyleTitleSmall,
		Sizing:    gogui.FillFit,
	})
}

// labelText is the fixed-width label column shared by kv rows.
func labelText(t gogui.Theme, label string) gogui.View {
	return gogui.Text(gogui.TextCfg{
		Text:      label,
		TextStyle: t.TextStyleLabel,
		MinWidth:  132,
		Sizing:    gogui.FixedFit,
	})
}

// kv is one label/value row with a fixed label column.  PaddingNone keeps the
// rows compact: without it every row pays the container's default padding,
// which made the cards read as half-empty.
func kv(t gogui.Theme, label, value string) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		Spacing:    gogui.SpacingMedium,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			labelText(t, label),
			gogui.Text(gogui.TextCfg{
				Text:      value,
				TextStyle: t.TextStyleBody,
				Sizing:    gogui.FillFit,
				Mode:      gogui.TextModeWrap,
			}),
		},
	})
}

// settingRow is a Fluent settings row: a title (with an optional description)
// on the left and the controlling widget on the right.  It is what gives a
// card its shape — the row earns the full card width instead of leaving the
// control stranded at one edge.
func settingRow(t gogui.Theme, title, desc string, control gogui.View) gogui.View {
	left := []gogui.View{bodyText(t, title)}
	if desc != "" {
		left = append(left, caption(t, desc))
	}

	return gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		Spacing:    gogui.SpacingMedium,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		VAlign:     gogui.VAlignMiddle,
		Content: []gogui.View{
			gogui.Column(gogui.ContainerCfg{
				Sizing:     gogui.FillFit,
				Spacing:    gogui.SpacingTight,
				Padding:    gogui.PaddingNone,
				SizeBorder: gogui.NoBorder,
				Content:    left,
			}),
			control,
		},
	})
}

// bodyText is a fill-width body line.
func bodyText(t gogui.Theme, s string) gogui.View {
	return gogui.Text(gogui.TextCfg{
		Text:      s,
		TextStyle: t.TextStyleBody,
		Sizing:    gogui.FillFit,
		Mode:      gogui.TextModeWrap,
	})
}

// statusVisualFor maps a snapshot onto the state the viewer sees.  Order is
// the whole point: trust gates everything, reachability next, then the enabled
// flag.  Dirty is a marker the lifecycle sets for the duration of an enabled
// session, so it must NEVER override an enabled state — it only means "Restore
// needed" when interception is NOT enabled (a genuine crash leftover).
func statusVisualFor(s snapshot, t gogui.Theme) statusVisual {
	switch {
	case s.trust == trustUnverified:
		return statusVisual{
			kind:          kindUnverified,
			headline:      "Unverified",
			detail:        "The status source could not be confirmed as the dns-client service, so its data is not shown.",
			headlineStyle: t.TextStyleError,
			bg:            t.ColorErrorSubtle,
			accent:        t.TextStyleError.Color,
		}
	case !s.running:
		if s.installed {
			return statusVisual{
				kind:          kindStopped,
				headline:      "Stopped",
				detail:        "The dns-client service is installed but not running.",
				headlineStyle: t.TextStyleTitleSmall,
				bg:            t.ColorPanel,
				accent:        t.TextStyleSecondary.Color,
			}
		}

		return statusVisual{
			kind:          kindStopped,
			headline:      "Not installed",
			detail:        "The dns-client service is not installed yet.",
			headlineStyle: t.TextStyleTitleSmall,
			bg:            t.ColorPanel,
			accent:        t.TextStyleSecondary.Color,
		}
	case enabled(s):
		if s.summary != nil && !s.summary.ConfigValid {
			return statusVisual{
				kind:          kindDegraded,
				headline:      "Needs attention",
				detail:        configErrorDetail(s.summary),
				headlineStyle: t.TextStyleWarning,
				bg:            t.ColorWarningSubtle,
				accent:        t.TextStyleWarning.Color,
			}
		}
		if !healthOK(s.status.Health) {
			return statusVisual{
				kind:          kindDegraded,
				headline:      "Needs attention",
				detail:        "The resolver is enabled but its last health check did not pass.",
				headlineStyle: t.TextStyleWarning,
				bg:            t.ColorWarningSubtle,
				accent:        t.TextStyleWarning.Color,
			}
		}

		// A healthy session keeps the neutral panel fill: the green dot and
		// headline carry the state, so the page is not a full-width wash.
		return statusVisual{
			kind:          kindRunning,
			headline:      "Protected",
			detail:        "DNS queries are handled by the dns-client loopback resolver.",
			headlineStyle: t.TextStyleSuccess,
			bg:            t.ColorPanel,
			accent:        t.TextStyleSuccess.Color,
		}
	case s.status != nil && s.status.Dirty:
		return statusVisual{
			kind:          kindDirty,
			headline:      "Restore needed",
			detail:        "Interception is still applied from a previous session. Use Restore now to clean up.",
			headlineStyle: t.TextStyleWarning,
			bg:            t.ColorWarningSubtle,
			accent:        t.TextStyleWarning.Color,
		}
	default:
		return statusVisual{
			kind:          kindStopped,
			headline:      "Interception off",
			detail:        "The service is running but DNS interception is turned off.",
			headlineStyle: t.TextStyleTitleSmall,
			bg:            t.ColorPanel,
			accent:        t.TextStyleSecondary.Color,
		}
	}
}

// enabled reports whether the status DTO says interception is on.  A nil
// status (service not running) is not enabled.
func enabled(s snapshot) bool {
	return s.status != nil && s.status.Enabled
}

// configErrorDetail renders the secret-free config error as one sentence.
func configErrorDetail(sum *ipc.ConfigSummary) (detail string) {
	if sum.ConfigError == "" {
		return "The configuration could not be loaded, so filtering and routing may not match it."
	}

	return "The configuration could not be loaded: " + sum.ConfigError + "."
}

// healthOK reports whether every health signal in the summary passed.  A nil
// health means the service has not reported one; that is not treated as a
// failure.
func healthOK(h *ipc.HealthSummary) bool {
	if h == nil {
		return true
	}

	return h.Ours && h.IPv4 && h.IPv6
}

// resolverID masks the resolver id: set or unset, never the value (SPEC §15.2).
func resolverID(set bool) (s string) {
	if set {
		return "•••• (set)"
	}

	return "Not set"
}

// displayState capitalizes an SCM state name for display.  The stored value
// stays lowercase — the DTO and its tests depend on that — and only the rendered
// text is title-cased: "running" -> "Running", "not installed" -> "Not installed".
func displayState(s string) string {
	s = fallback(s, "unknown")

	return strings.ToUpper(s[:1]) + s[1:]
}

// humanBytes renders a byte count in binary units.
func humanBytes(n int) (s string) {
	if n <= 0 {
		return "—"
	}

	const unit = 1024
	units := []string{"B", "KiB", "MiB", "GiB"}
	f := float64(n)
	i := 0
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[i])
	}

	return fmt.Sprintf("%.1f %s", f, units[i])
}

func fallback(s, def string) (out string) {
	if strings.TrimSpace(s) == "" {
		return def
	}

	return s
}

func yesNo(b bool) (s string) {
	if b {
		return "Yes"
	}

	return "No"
}

func enabledWord(b bool) (s string) {
	if b {
		return "enabled"
	}

	return "disabled"
}
