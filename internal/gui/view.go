//go:build gui

package gui

import (
	"fmt"
	"strings"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/daika7ana/sigil-dns/internal/ipc"
	"github.com/daika7ana/sigil-dns/internal/version"
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
	pageLogging
	pageAbout
)

// statusVisual is the resolved hero for a state: a headline, one calm
// sentence, and its theme colors.  Normal states stay quiet — only a genuine
// anomaly takes a warning or error tone.
type statusVisual struct {
	headline      string
	detail        string
	headlineStyle gogui.TextStyle
	pill          gogui.Color // headline pill fill
	accent        gogui.Color // the status dot and illustration ink
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
	// contentMaxWidth caps every page's reading column.  The dashboard grid and
	// the forms read differently, but one cap keeps the page edges from
	// shifting when the user switches pages.
	contentMaxWidth float32 = 1120
	// contentPad is the inset around the reading column and inside a page
	// column, trimmed a few px tighter than the PaddingMedium (14) rung on both
	// axes.
	contentPad float32 = 10
	// brandSize is the app mark's square edge, in logical pixels.  It matches
	// the icon's 32px frame, so a 100% display draws the mark 1:1.
	brandSize float32 = 32
	// navItemMinHeight is the navigation item's height floor, so the larger
	// glyph and its label sit comfortably and the hit target stays a good size.
	navItemMinHeight float32 = 40
	// navIconWidth is the fixed icon column inside an item.  Every glyph is
	// centred in it, so all labels start at one X whatever the glyph's own
	// width — a fixed column rather than per-icon padding.
	navIconWidth float32 = 20
	// headerMinHeight floors the header band to an even integer so its bottom
	// hairline lands on a whole pixel and reads the same weight as the nav-pane
	// divider (a fractional band would antialias it into two faint rows).
	headerMinHeight float32 = 64

	// Status dashboard geometry.  The hero takes the left of a two-column top
	// row, the connection panel a fixed right column, and the three secondary
	// cards split the row below.  Both rows share one card minimum height and
	// one gutter, so the dashboard reads as a single grid; FillFill sizing on a
	// row's cards keeps their heights equal when one card's content grows.
	statusStackWidth            = 1160
	statusInfoWidth     float32 = 320
	statusTopMinHeight  float32 = 200
	statusCardMinHeight float32 = 200
	statusPillHeight    float32 = 32
	statusGraphWidth    float32 = 240
	statusGraphHeight   float32 = 168
)

// view builds the window for the current state.  It runs on the main thread
// during the frame pass.
func view(w *gogui.Window) gogui.View {
	app := gogui.State[App](w)
	t := gogui.CurrentTheme()
	windowWidth, _ := w.WindowSize()
	mark := markPixels(w.BackingScale)
	// Idempotent: it returns immediately while the window DPI is unchanged,
	// and re-installs the HICONs after a per-monitor DPI change.
	installWindowIcons()

	content := []gogui.View{
		header(t, app, mark),
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
				contentArea(t, app, windowWidth, mark),
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

// pageColumn wraps a page's content in the shared scrollable, inset column.
func pageColumn(id string, content []gogui.View) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		ID: id, Sizing: gogui.FillFill, MaxWidth: contentMaxWidth,
		Padding: pagePadding(), Spacing: gogui.SpacingMedium,
		SizeBorder: gogui.NoBorder, Scrollable: true, Content: content,
	})
}

// withEditNotice prepends the trust/offline banner to an editor page's content.
func withEditNotice(t gogui.Theme, app *App, content []gogui.View) []gogui.View {
	if v := editNotice(t, app); v != nil {
		return append([]gogui.View{v}, content...)
	}

	return content
}

// contentArea centers a capped reading column in the space beside the rail.
// HAlignCenter places the capped child box; MaxWidth on the child is what caps
// it, so the column stays readable at any window width.
func contentArea(t gogui.Theme, app *App, windowWidth, mark int) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		ID:         "gui:contentwrap",
		Sizing:     gogui.FillFill,
		HAlign:     gogui.HAlignCenter,
		Padding:    gogui.NewPadding(contentPad, contentPad, contentPad, contentPad),
		Spacing:    gogui.NoSpacing,
		SizeBorder: gogui.NoBorder,
		Content:    []gogui.View{pageContent(t, app, windowWidth, mark)},
	})
}

// pageContent is the body of the selected page.
func pageContent(t gogui.Theme, app *App, windowWidth, mark int) gogui.View {
	switch app.page {
	case pageSettings:
		return settingsPage(t, app, windowWidth)
	case pageRules:
		return rulesPage(t, app, windowWidth)
	case pageLogging:
		return loggingPage(t, app, windowWidth)
	case pageAbout:
		return aboutPage(t, app, mark)
	default:
		return statusPage(t, app, windowWidth)
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
			navItem(t, app, pageLogging, "Logging", gogui.IconDocument),
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
			navIcon(t, p, icon, selected),
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
// NavigationView icon beside the 14px label, not a small dot.  The glyph is
// centred in the fixed navIconWidth column, so every label starts at the same X.
func navIcon(t gogui.Theme, p pageKind, icon string, selected bool) gogui.View {
	ts := t.TextStyleIconLarge
	ts.Color = t.TextStyleSecondary.Color
	if selected {
		ts.Color = t.ColorAccent
	}

	return gogui.Row(gogui.ContainerCfg{
		ID:         "gui:nav:icon:" + pageName(p),
		Width:      navIconWidth,
		Sizing:     gogui.FixedFit,
		HAlign:     gogui.HAlignCenter,
		VAlign:     gogui.VAlignMiddle,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			gogui.Text(gogui.TextCfg{
				Text:      icon,
				TextStyle: ts,
				Sizing:    gogui.FitFit,
			}),
		},
	})
}

// pageName is the stable suffix for a page's widget ID.
func pageName(p pageKind) (name string) {
	switch p {
	case pageSettings:
		return "settings"
	case pageRules:
		return "rules"
	case pageLogging:
		return "logging"
	case pageAbout:
		return "about"
	default:
		return "status"
	}
}

// About metadata.  Plain constants, so the page needs no service read or config
// file and renders the same in every state.
const (
	appName        = "Sigil DNS"
	appAuthor      = "daika7ana"
	appAuthorURL   = "https://github.com/daika7ana"
	appRepoURL     = "https://github.com/daika7ana/sigil-dns"
	appLicense     = "GPLv3"
	appLicenseNote = "This program links AdguardTeam/urlfilter, which is licensed under GPL-3.0."
)

// aboutPage is the read-only About screen, laid out as three settings cards:
// identity, versions and license, then links.  The author and repository values
// are links that open in the default browser.
func aboutPage(t gogui.Theme, app *App, mark int) gogui.View {
	identity := []gogui.View{
		gogui.Row(gogui.ContainerCfg{
			ID:         "gui:about:identity",
			Sizing:     gogui.FillFit,
			Spacing:    gogui.SpacingMedium,
			Padding:    gogui.PaddingNone,
			SizeBorder: gogui.NoBorder,
			VAlign:     gogui.VAlignMiddle,
			Content: []gogui.View{
				brandMark("gui:about:mark", mark),
				gogui.Column(gogui.ContainerCfg{
					Sizing:     gogui.FillFit,
					Spacing:    gogui.SpacingTight,
					Padding:    gogui.PaddingNone,
					SizeBorder: gogui.NoBorder,
					Content: []gogui.View{
						gogui.Text(gogui.TextCfg{
							Text:      "Encrypted DNS client for Windows",
							TextStyle: t.TextStyleSecondary,
							Sizing:    gogui.FillFit,
							Mode:      gogui.TextModeWrap,
						}),
					},
				}),
			},
		}),
	}

	serviceVersion, protocol := aboutVersions(app)
	versions := []gogui.View{
		kv(t, "Client version", version.Version),
		kv(t, "Service version", serviceVersion),
		kv(t, "Interface version", protocol),
		kv(t, "License", appLicense),
		// The note lives under the license row it qualifies, not in the identity
		// card, where a caption under a larger tagline read as inverted hierarchy.
		gogui.Text(gogui.TextCfg{
			ID:        "gui:about:license",
			Text:      appLicenseNote,
			TextStyle: captionStyle(t),
			Sizing:    gogui.FillFit,
			Mode:      gogui.TextModeWrap,
		}),
	}
	links := []gogui.View{
		kvLink(t, "gui:about:author", "Author", appAuthor, appAuthorURL),
		kvLink(t, "gui:about:repo", "Repository", appRepoURL, appRepoURL),
	}

	return pageColumn("gui:about", []gogui.View{
		card(t, "gui:card:about", appName, identity),
		card(t, "gui:card:aboutversion", "Version & license", versions),
		card(t, "gui:card:aboutlinks", "Links", links),
	})
}

// aboutVersions resolves the client-facing build line for the About page: the
// live service version when the service reported one, and the compiled IPC
// protocol otherwise.  It never invents a value — a missing service read shows
// an em dash.
func aboutVersions(app *App) (service, protocol string) {
	service = "—"
	protocol = fmt.Sprintf("%d", version.ProtocolVersion)
	if app.snap.info != nil {
		service = fallback(app.snap.info.AppVersion, version.Version)
		protocol = fmt.Sprintf("%d", app.snap.info.ProtocolVersion)
	}

	return service, protocol
}

// kvLink is a kv row whose value is a clickable link.  The label column matches
// kv so the About rows stay aligned; the value is link-styled accent text.
func kvLink(t gogui.Theme, id, label, text, url string) gogui.View {
	return kvRow(t, label, linkButton(t, id, text, url))
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

// statusPage is the status dashboard in ONE scrollable column: a hero and
// connection panel on top, then filtering, filter lists, and cache.  Every
// value comes from the effective config summary (live, or the cached last-good
// one while the service is stopped); an unverified source shows the hero alone.
// The Start/Stop control lives in the Settings service card; the state badge
// lives in the header.
func statusPage(t gogui.Theme, app *App, windowWidth int) gogui.View {
	v := statusVisualFor(app.snap, t)

	// Trust gates the whole dashboard.  A verified source whose summary read
	// has not landed yet still renders the cards, whose rows fall back to an em
	// dash — only an unverified source shows the hero alone.
	if !canEdit(app.snap) {
		return pageColumn("gui:content", []gogui.View{statusHero(t, v, nil, false)})
	}

	return pageColumn("gui:content", []gogui.View{statusDashboard(t, v, app.summary(), windowWidth)})
}

// statusDashboard arranges the Status cards.  Above statusStackWidth the top
// row splits hero/connection and the bottom row splits three ways; below it the
// top row stacks, the first two bottom cards stay side by side, and the cache
// card takes the full width.
func statusDashboard(t gogui.Theme, v statusVisual, sum *ipc.ConfigSummary, windowWidth int) gogui.View {
	stacked := windowWidth < statusStackWidth

	// On a wide row both cards fill the row height, so hero and connection
	// share one baseline even when one of them has more content.  The paired
	// bottom cards always fill; the full-width cache card does not need to.
	hero := statusHero(t, v, sum, !stacked)
	connection := connectionSection(sum, t, !stacked)
	filtering := filteringSection(sum, t, true)
	lists := filterListsSection(sum, t, true)
	cache := cacheSection(sum, t, !stacked)

	var top, bottom gogui.View
	if stacked {
		top = gogui.Column(gogui.ContainerCfg{
			ID: "gui:status:top", Sizing: gogui.FillFit,
			Padding: gogui.PaddingNone, Spacing: gogui.SpacingMedium, SizeBorder: gogui.NoBorder,
			Content: []gogui.View{hero, connection},
		})
		bottom = gogui.Column(gogui.ContainerCfg{
			ID: "gui:status:bottom", Sizing: gogui.FillFit,
			Padding: gogui.PaddingNone, Spacing: gogui.SpacingMedium, SizeBorder: gogui.NoBorder,
			Content: []gogui.View{
				gogui.Row(gogui.ContainerCfg{
					Sizing: gogui.FillFit, Padding: gogui.PaddingNone,
					Spacing: gogui.SpacingMedium, SizeBorder: gogui.NoBorder,
					Content: []gogui.View{filtering, lists},
				}),
				cache,
			},
		})
	} else {
		top = gogui.Row(gogui.ContainerCfg{
			ID: "gui:status:top", Sizing: gogui.FillFit,
			Padding: gogui.PaddingNone, Spacing: gogui.SpacingMedium, SizeBorder: gogui.NoBorder,
			Content: []gogui.View{hero, fixedWidth(statusInfoWidth, connection)},
		})
		bottom = gogui.Row(gogui.ContainerCfg{
			ID: "gui:status:bottom", Sizing: gogui.FillFit,
			Padding: gogui.PaddingNone, Spacing: gogui.SpacingMedium, SizeBorder: gogui.NoBorder,
			Content: []gogui.View{filtering, lists, cache},
		})
	}

	return gogui.Column(gogui.ContainerCfg{
		ID: "gui:status:grid", Sizing: gogui.FillFit,
		Padding: gogui.PaddingNone, Spacing: gogui.SpacingMedium, SizeBorder: gogui.NoBorder,
		Content: []gogui.View{top, bottom},
	})
}

// fixedWidth wraps a card in an inert fixed-width column, so the top row keeps
// its two-to-one split without a second card-sizing path.
func fixedWidth(width float32, content gogui.View) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		Width: width, Sizing: gogui.FixedFill,
		Padding: gogui.PaddingNone, SizeBorder: gogui.NoBorder,
		Content: []gogui.View{content},
	})
}

// statusHero is the active-state surface: a soft state-tinted gradient, the
// headline pill, one calm sentence, the profile chips, and a shield
// illustration.  It carries every state's messaging; the tint and pill follow
// the state's semantic colour.
func statusHero(t gogui.Theme, v statusVisual, sum *ipc.ConfigSummary, fill bool) gogui.View {
	body := []gogui.View{
		caption(t, "Active status"),
		statusPill(t, v),
		gogui.Text(gogui.TextCfg{
			ID:        "gui:banner:detail",
			Text:      v.detail,
			TextStyle: t.TextStyleSecondary,
			Mode:      gogui.TextModeWrap,
			Sizing:    gogui.FillFit,
		}),
	}
	if chips := heroChips(t, sum); chips != nil {
		body = append(body, chips)
	}

	return gogui.Column(gogui.ContainerCfg{
		ID:          "gui:banner",
		Sizing:      statusBoxSizing(fill),
		MinHeight:   statusTopMinHeight,
		Color:       t.ColorPanel,
		Gradient:    heroGradient(t, v),
		ColorBorder: t.ColorBorder,
		SizeBorder:  gogui.BorderThin,
		Radius:      gogui.RadiusLarge,
		Padding:     t.PaddingLarge,
		VAlign:      gogui.VAlignMiddle,
		Content: []gogui.View{
			gogui.Row(gogui.ContainerCfg{
				Sizing:     gogui.FillFit,
				Padding:    gogui.PaddingNone,
				Spacing:    gogui.SpacingMedium,
				SizeBorder: gogui.NoBorder,
				VAlign:     gogui.VAlignMiddle,
				Content: []gogui.View{
					gogui.Column(gogui.ContainerCfg{
						ID: "gui:banner:body", Sizing: gogui.FillFit,
						Padding: gogui.PaddingNone, Spacing: gogui.SpacingSmall, SizeBorder: gogui.NoBorder,
						Content: body,
					}),
					statusGraph(v),
				},
			}),
		},
	})
}

// heroGradient is the hero's soft two-stop tint: mint into pale blue for a
// protected session, the state's caution hue into the panel for an anomaly, and
// a quiet panel-to-accent wash for a stopped session.  Interpolating the theme's
// subtle colors over the panel keeps the stops opaque on both polarities.
func heroGradient(t gogui.Theme, v statusVisual) *gogui.GradientDef {
	panel := t.ColorPanel
	accentTint := t.ColorAccentSubtle.Over(panel)
	start, end := panel, accentTint

	switch v.kind {
	case kindRunning:
		start = t.ColorSuccessSubtle.Over(panel)
	case kindDegraded, kindDirty:
		start = t.ColorWarningSubtle.Over(panel)
		end = panel
	case kindUnverified:
		start = t.ColorErrorSubtle.Over(panel)
		end = panel
	}

	return &gogui.GradientDef{
		Direction: gogui.GradientToBottomRight,
		Stops: []gogui.GradientStop{
			{Color: start, Pos: 0},
			{Color: end, Pos: 1},
		},
	}
}

// statusPill is the state headline as a Fluent capsule: the status dot and the
// headline in a state-tinted pill.
func statusPill(t gogui.Theme, v statusVisual) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		ID:          "gui:banner:title",
		Sizing:      gogui.FitFit,
		MinHeight:   statusPillHeight,
		Color:       v.pill,
		ColorBorder: v.accent.WithOpacity(0.35),
		SizeBorder:  gogui.BorderThin,
		Radius:      gogui.RadiusPx(statusPillHeight / 2),
		Padding:     t.PaddingSmall,
		Spacing:     gogui.SpacingSmall,
		VAlign:      gogui.VAlignMiddle,
		Content: []gogui.View{
			statusIndicator(v),
			gogui.Text(gogui.TextCfg{
				ID:        "gui:banner:headline",
				Text:      v.headline,
				TextStyle: v.headlineStyle,
				Sizing:    gogui.FitFit,
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

// heroChips shows the active provider, variant, and transport as small pills.
// They are display-only labels from the sanitized summary, never a control.
func heroChips(t gogui.Theme, sum *ipc.ConfigSummary) gogui.View {
	if sum == nil {
		return nil
	}

	labels := []string{
		providerLabel(sum.Provider),
		variantNameLabel(sum.Provider, sum.Variant),
		protocolChip(sum.Protocol),
	}
	chips := make([]gogui.View, 0, len(labels))
	for _, label := range labels {
		if label = strings.TrimSpace(label); label != "" {
			chips = append(chips, chip(t, label))
		}
	}
	if len(chips) == 0 {
		return nil
	}

	return gogui.Row(gogui.ContainerCfg{
		ID: "gui:banner:chips", Sizing: gogui.FillFit,
		Padding: gogui.PaddingNone, Spacing: gogui.SpacingSmall, SizeBorder: gogui.NoBorder,
		VAlign:  gogui.VAlignMiddle,
		Content: chips,
	})
}

// chip is one quiet label pill on the hero gradient.
func chip(t gogui.Theme, label string) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		Sizing: gogui.FitFit, MinHeight: 26,
		Color: t.ColorPanel, ColorBorder: t.ColorBorder, SizeBorder: gogui.BorderThin,
		Radius: gogui.RadiusPx(13), Padding: gogui.PaddingSmall,
		VAlign: gogui.VAlignMiddle,
		Content: []gogui.View{
			gogui.Text(gogui.TextCfg{
				Text:      label,
				TextStyle: t.TextStyleCaption,
				Sizing:    gogui.FitFit,
				Clip:      true,
			}),
		},
	})
}

// protocolChip shortens a transport for a chip.  It keeps the DTO value as the
// source and changes only the rendered casing.
func protocolChip(p string) (label string) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "doh":
		return "DoH"
	case "dot":
		return "DoT"
	case "doq":
		return "DoQ"
	default:
		return strings.ToUpper(strings.TrimSpace(p))
	}
}

// connectionSection renders the active profile's display-only labels and a
// masked resolver id (SPEC §15.2: never the id itself).  Fill lets the card
// take the top row's full height, so the hero and connection panel end on one
// baseline instead of the panel leaving a dead strip under its rows.
func connectionSection(sum *ipc.ConfigSummary, t gogui.Theme, fill bool) gogui.View {
	rows := []gogui.View{}
	if sum == nil {
		rows = append(rows, kv(t, "Profile", "—"))
	} else {
		rows = append(rows,
			kv(t, "Profile", fallback(sum.Profile, "—")),
			kv(t, "Provider", fallback(providerLabel(sum.Provider), "—")),
			kv(t, "Variant", fallback(variantNameLabel(sum.Provider, sum.Variant), "—")),
			kv(t, "Protocol", fallback(protocolChip(sum.Protocol), "—")),
			kv(t, "Resolver ID", resolverID(sum.IDSet)),
		)
	}

	return cardBoxSized(t, "gui:card:connection", "Connection Info", rows,
		statusTopMinHeight, statusBoxSizing(fill))
}

// filteringSection renders filtering settings and rule counts.  The list count
// is deliberately absent: the Filter lists card names every list beside it, so
// a count here duplicated that card's purpose.
func filteringSection(sum *ipc.ConfigSummary, t gogui.Theme, fill bool) gogui.View {
	rows := []gogui.View{}
	if sum == nil {
		rows = append(rows, kv(t, "Status", "—"))
	} else {
		rows = append(rows,
			// The card title already says Filtering, so the row names the
			// state, not the feature again.
			kv(t, "Enabled", yesNo(sum.FilteringEnabled)),
			kv(t, "Block response", fallback(blockResponseLabel(sum.BlockResponse), "—")),
			kv(t, "Per-domain rules", fmt.Sprintf("%d", sum.PerDomainCount)),
		)
	}

	return cardBoxSized(t, "gui:card:filtering", "Filtering", rows,
		statusCardMinHeight, statusBoxSizing(fill))
}

// blockResponseLabel is the display casing for the config's block response,
// so the Status card matches the Settings selector (NXDOMAIN, not nxdomain).
func blockResponseLabel(resp string) (label string) {
	if strings.EqualFold(resp, "nxdomain") {
		return "NXDOMAIN"
	}

	return resp
}

// filterListsSection names every configured list and its enabled state.  The
// host is clipped rather than wrapped so a long host cannot grow the card.
func filterListsSection(sum *ipc.ConfigSummary, t gogui.Theme, fill bool) gogui.View {
	rows := []gogui.View{}
	if sum == nil || len(sum.Lists) == 0 {
		rows = append(rows, kv(t, "Lists", "—"))
	} else {
		for _, l := range sum.Lists {
			rows = append(rows, filterListRow(t, fallback(l.Host, "(list)"), l.Enabled))
		}
	}

	return cardBoxSized(t, "gui:card:filterlists", "Filter lists", rows,
		statusCardMinHeight, statusBoxSizing(fill))
}

// filterListRow is one filter list: a state dot, the clipped host, and its word.
func filterListRow(t gogui.Theme, host string, enabled bool) gogui.View {
	dot := t.TextStyleSecondary.Color
	wordStyle := t.TextStyleCaption
	if enabled {
		dot = t.TextStyleSuccess.Color
		wordStyle.Color = t.TextStyleSuccess.Color
	}

	return gogui.Row(gogui.ContainerCfg{
		Sizing: gogui.FillFit, Padding: gogui.PaddingNone,
		Spacing: gogui.SpacingSmall, SizeBorder: gogui.NoBorder, VAlign: gogui.VAlignMiddle,
		Content: []gogui.View{
			gogui.Circle(gogui.ContainerCfg{
				Width: 6, Height: 6, Sizing: gogui.FixedFixed,
				Color: dot, Padding: gogui.PaddingNone, SizeBorder: gogui.NoBorder,
			}),
			gogui.Text(gogui.TextCfg{
				Text: host, TextStyle: t.TextStyleBody,
				Sizing: gogui.FillFit, Clip: true,
			}),
			gogui.Text(gogui.TextCfg{
				Text: enabledWord(enabled), TextStyle: wordStyle, Sizing: gogui.FitFit,
			}),
		},
	})
}

// cacheSection renders cache and bootstrap settings.  Bootstrap keeps its
// stacked value block (an IPv6 literal is too wide for the value column), but
// its label sits in the same left column and rhythm as every other kv label,
// so the card reads as one grid rather than a second, offset layout.
func cacheSection(sum *ipc.ConfigSummary, t gogui.Theme, fill bool) gogui.View {
	rows := []gogui.View{}
	if sum == nil {
		rows = append(rows, kv(t, "Status", "—"))
	} else {
		rows = append(rows,
			kv(t, "Cache", yesNo(sum.CacheEnabled)),
			kv(t, "Cache size", humanBytes(sum.CacheSizeBytes)),
			labelTextID(t, "gui:status:bootstrap:label", "Bootstrap"),
			bootstrapValue(t, sum.Bootstrap),
		)
	}

	return cardBoxSized(t, "gui:card:cache", "Cache & bootstrap", rows,
		statusCardMinHeight, statusBoxSizing(fill))
}

// bootstrapValue stacks the bootstrap addresses one per line.  A joined,
// comma-separated string is a single unbreakable run to the layout, so a long
// list would widen this card past the rest of the bottom row.
func bootstrapValue(t gogui.Theme, addrs []string) gogui.View {
	lines := make([]gogui.View, 0, max(len(addrs), 1))
	for _, addr := range addrs {
		lines = append(lines, bootstrapLine(t, addr))
	}
	if len(lines) == 0 {
		lines = append(lines, bootstrapLine(t, "—"))
	}

	return gogui.Column(gogui.ContainerCfg{
		ID:         "gui:status:bootstrap:value",
		Sizing:     gogui.FillFit,
		Padding:    gogui.PaddingNone,
		Spacing:    gogui.SpacingTight,
		Content:    lines,
		SizeBorder: gogui.NoBorder,
	})
}

func bootstrapLine(t gogui.Theme, line string) gogui.View {
	// A literal has no break opportunity, so wrap cannot help; clip is the
	// guard that keeps an over-long address from widening the card.
	return gogui.Text(gogui.TextCfg{
		Text: line, TextStyle: t.TextStyleBody,
		Sizing: gogui.FillFit, Clip: true,
	})
}

// header is the app's identity band: the app mark, the title with its service
// state badge, and the status caption on the left, and the only real header
// action (Restore, when a crash leftover needs cleaning up) on the right.  It
// takes the background tone — chrome is background, cards are panel.  The
// Auto/Light/Dark picker lives in Settings → Appearance.
func header(t gogui.Theme, app *App, mark int) gogui.View {
	ps := powerStateFor(t, app)

	caption := updatedText(app)
	switch {
	case app.busy:
		caption = app.busyNote
	case ps.note != "":
		caption = ps.note
	}

	controls := []gogui.View{}
	if ps.restore {
		controls = append(controls, gogui.Button(gogui.ButtonCfg{
			ID:       "gui:action:restore",
			Variant:  gogui.ButtonPrimary,
			Label:    "Restore now",
			Disabled: app.busy,
			OnClick:  func(ctx gogui.EventCtx) { runAction(ctx, actionRestore) },
		}))
	}

	return gogui.Row(gogui.ContainerCfg{
		ID:         "gui:header",
		Sizing:     gogui.FillFit,
		Color:      t.ColorBackground,
		MinHeight:  headerMinHeight,
		Padding:    gogui.NewPadding(t.PaddingMedium.Top, t.PaddingLarge.Left, t.PaddingMedium.Bottom, t.PaddingLarge.Right),
		Spacing:    gogui.SpacingMedium,
		VAlign:     gogui.VAlignMiddle,
		SizeBorder: gogui.NoBorder,
		Content:    append([]gogui.View{brand(t, app, caption, mark)}, controls...),
	})
}

// brand is the app identity block: a mark, the title with its service state
// badge, and the status caption below.  It fills the header's free space so the
// actions land on the right.
func brand(t gogui.Theme, app *App, caption string, mark int) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		Padding:    gogui.PaddingNone,
		Spacing:    gogui.SpacingMedium,
		SizeBorder: gogui.NoBorder,
		VAlign:     gogui.VAlignMiddle,
		Content: []gogui.View{
			brandMark("gui:brand", mark),
			gogui.Column(gogui.ContainerCfg{
				Sizing:     gogui.FitFit,
				Padding:    gogui.PaddingNone,
				Spacing:    gogui.SpacingTight,
				SizeBorder: gogui.NoBorder,
				Content: []gogui.View{
					gogui.Row(gogui.ContainerCfg{
						ID:         "gui:brand:title",
						Sizing:     gogui.FitFit,
						Padding:    gogui.PaddingNone,
						Spacing:    gogui.SpacingSmall,
						SizeBorder: gogui.NoBorder,
						VAlign:     gogui.VAlignMiddle,
						Content: []gogui.View{
							gogui.Text(gogui.TextCfg{
								Text:      "Sigil DNS",
								TextStyle: t.TextStyleTitle,
								Sizing:    gogui.FitFit,
							}),
							serviceBadge(app.snap),
						},
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

// brandMark is the app icon at the header's mark size.  It draws the embedded
// icon frame for the display scale, so the header and About page cannot drift
// from the title-bar and tray icon.
func brandMark(id string, px int) gogui.View {
	return gogui.Image(gogui.ImageCfg{
		ID:     id,
		Src:    appIconSource(px),
		Width:  brandSize,
		Height: brandSize,
	})
}

// markPixels is the header mark's size in device pixels.  The icon ships
// pre-rendered frames at these sizes, so the mark is drawn 1:1 instead of
// minifying the 256px source.  A first frame can arrive before the backend
// reports a scale; 2x keeps it sharp and the next layout rebuild corrects it.
func markPixels(scale float32) int {
	if scale <= 0 {
		scale = 2
	}

	return int(brandSize*scale + 0.5)
}

// powerStateFor resolves the header's Restore affordance and guidance note for
// the current snapshot.
func powerStateFor(t gogui.Theme, app *App) powerState {
	return planPower(statusVisualFor(app.snap, t).kind,
		app.snap.installed, app.snap.running, app.busy, canEdit(app.snap))
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

	content := []gogui.View{
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
					A11YCfg: gogui.A11YCfg{A11YLabel: "Dismiss notification"},
					OnClick: dismissToast,
				}),
			},
		}),
	}
	// A routine outcome is title-only: no body node, so no empty gap under it.
	if body := alertBody(n.title, n.body); body != "" {
		content = append(content, gogui.Text(gogui.TextCfg{
			ID:        "gui:toast:body",
			Text:      body,
			TextStyle: t.TextStyleSecondary,
			Mode:      gogui.TextModeWrap,
			Sizing:    gogui.FillFit,
		}))
	}

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
		Content:      content,
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

// card is a Fluent surface: a rounded panel with a hairline border and a
// heading.  Cards separate from the page by fill value, never by a shadow.  It
// uses a plain filled/outlined Column with an in-flow heading Text rather than
// the toolkit's group-box Title: the group-box legend is drawn outside the
// scrollable clip, so scrolled cards painted their legend at the scroll origin
// and overlapped the banner, and its TitleBG struck a line through the text.
func card(t gogui.Theme, id, title string, rows []gogui.View) gogui.View {
	return cardBox(t, id, title, rows, 0)
}

// cardBox is card with a minimum height.  The Status dashboard uses it to give
// a row's cards a shared baseline without a second card style.
func cardBox(t gogui.Theme, id, title string, rows []gogui.View, minHeight float32) gogui.View {
	return cardBoxSized(t, id, title, rows, minHeight, gogui.FillFit)
}

// cardBoxSized is cardBox with an explicit Sizing.  Status rows pass FillFill
// so every card in a row takes the row's height; forms stay content-sized.
func cardBoxSized(t gogui.Theme, id, title string, rows []gogui.View, minHeight float32, sizing gogui.Sizing) gogui.View {
	content := make([]gogui.View, 0, len(rows)+1)
	content = append(content, cardTitle(t, title))
	content = append(content, rows...)

	return gogui.Column(gogui.ContainerCfg{
		ID:          id,
		Sizing:      sizing,
		MinHeight:   minHeight,
		Color:       t.ColorPanel,
		ColorBorder: t.ColorBorder,
		SizeBorder:  gogui.BorderThin,
		Radius:      gogui.RadiusLarge,
		Padding:     t.PaddingLarge,
		Spacing:     gogui.SpacingMedium,
		Content:     content,
	})
}

// statusBoxSizing is FillFill for a card that must stretch to its row's height,
// FillFit for one that only needs its own content.
func statusBoxSizing(fill bool) gogui.Sizing {
	if fill {
		return gogui.FillFill
	}

	return gogui.FillFit
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
	return labelTextID(t, "", label)
}

// labelTextID is labelText with a widget ID, for a label a test or a caller
// needs to find on its own.
func labelTextID(t gogui.Theme, id, label string) gogui.View {
	return gogui.Text(gogui.TextCfg{
		ID:        id,
		Text:      label,
		TextStyle: t.TextStyleLabel,
		MinWidth:  132,
		Sizing:    gogui.FixedFit,
	})
}

// kvRow is the shared label/value row shell with a fixed label column.
func kvRow(t gogui.Theme, label string, value gogui.View) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		Spacing:    gogui.SpacingMedium,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			labelText(t, label),
			value,
		},
	})
}

// kv is one label/value row with a fixed label column.  PaddingNone keeps the
// rows compact: without it every row pays the container's default padding,
// which made the cards read as half-empty.
func kv(t gogui.Theme, label, value string) gogui.View {
	return kvRow(t, label, gogui.Text(gogui.TextCfg{
		Text:      value,
		TextStyle: t.TextStyleBody,
		Sizing:    gogui.FillFit,
		Mode:      gogui.TextModeWrap,
	}))
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
			detail:        "The status source could not be confirmed as the Sigil DNS service, so its data is not shown.",
			headlineStyle: statusHeadlineStyle(t.TextStyleTitleSmall, t.TextStyleError.Color),
			pill:          overPanel(t, t.ColorErrorSubtle),
			accent:        t.TextStyleError.Color,
		}
	case !s.running:
		if s.installed {
			return stoppedVisual(t, "Stopped", "The Sigil DNS service is installed but not running.")
		}

		return stoppedVisual(t, "Not installed", "The Sigil DNS service is not installed yet.")
	case enabled(s):
		if s.summary != nil && !s.summary.ConfigValid {
			return degradedVisual(t, configErrorDetail(s.summary))
		}
		if !healthOK(s.status.Health) {
			return degradedVisual(t, "The resolver is enabled but its last health check did not pass.")
		}

		// The green pill and illustration carry a healthy session; the tint is
		// the mint-to-blue wash behind them.
		return statusVisual{
			kind:          kindRunning,
			headline:      "Protected",
			detail:        "DNS queries are handled locally by Sigil DNS.",
			headlineStyle: statusHeadlineStyle(t.TextStyleTitleSmall, t.TextStyleSuccess.Color),
			pill:          overPanel(t, t.ColorSuccessSubtle),
			accent:        t.TextStyleSuccess.Color,
		}
	case s.status != nil && s.status.Dirty:
		return statusVisual{
			kind:          kindDirty,
			headline:      "Restore needed",
			detail:        "Your DNS settings are still redirected from a previous session. Use Restore now to clean up.",
			headlineStyle: statusHeadlineStyle(t.TextStyleTitleSmall, t.TextStyleWarning.Color),
			pill:          overPanel(t, t.ColorWarningSubtle),
			accent:        t.TextStyleWarning.Color,
		}
	default:
		return stoppedVisual(t, "Protection off", "The service is running but DNS protection is turned off.")
	}
}

// stoppedVisual covers the quiet stopped states: the neutral pill and dot.
func stoppedVisual(t gogui.Theme, headline, detail string) statusVisual {
	return statusVisual{
		kind:          kindStopped,
		headline:      headline,
		detail:        detail,
		headlineStyle: t.TextStyleTitleSmall,
		pill:          t.ColorActive,
		accent:        t.TextStyleSecondary.Color,
	}
}

// statusHeadlineStyle lifts a semantic colour onto a heading role, so the pill
// carries the state without inventing a new text size.
func statusHeadlineStyle(base gogui.TextStyle, color gogui.Color) gogui.TextStyle {
	base.Color = color

	return base
}

// overPanel composites a translucent theme tint onto the card panel, so a pill
// or gradient stop stays opaque over the gradient beneath it.
func overPanel(t gogui.Theme, tint gogui.Color) gogui.Color {
	return tint.Over(t.ColorPanel)
}

// degradedVisual is the "Needs attention" hero shared by the invalid-config
// and failed-health states.
func degradedVisual(t gogui.Theme, detail string) statusVisual {
	return statusVisual{
		kind:          kindDegraded,
		headline:      "Needs attention",
		detail:        detail,
		headlineStyle: statusHeadlineStyle(t.TextStyleTitleSmall, t.TextStyleWarning.Color),
		pill:          overPanel(t, t.ColorWarningSubtle),
		accent:        t.TextStyleWarning.Color,
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
