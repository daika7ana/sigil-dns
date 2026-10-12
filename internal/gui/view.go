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
	pageAbout
)

// statusVisual is the resolved banner for a state: a headline, one calm
// sentence, and its theme colors.  Normal states stay quiet — only a genuine
// anomaly takes a warning or error tone.
type statusVisual struct {
	headline      string
	detail        string
	headlineStyle gogui.TextStyle
	bg            gogui.Color // banner wash
	accent        gogui.Color // the dot
	kind          statusKind
}

// view builds the window for the current state.  It runs on the main thread
// during the frame pass.
func view(w *gogui.Window) gogui.View {
	app := gogui.State[App](w)
	t := gogui.CurrentTheme()
	windowWidth, _ := w.WindowSize()

	content := []gogui.View{
		header(t, app),
		gogui.Row(gogui.ContainerCfg{
			ID:      "gui:body",
			Sizing:  gogui.FillFill,
			Spacing: gogui.SpacingMedium,
			Content: []gogui.View{
				navRail(t, app),
				pageContent(t, app, windowWidth),
			},
		}),
	}
	// The toast is a floating layer, not an in-flow card: it is lifted out of
	// the tree and painted bottom-right over the page.
	if app.toast != nil {
		content = append(content, toastOverlay(t, app))
	}

	return gogui.Column(gogui.ContainerCfg{
		Sizing: gogui.FillFill,
		Color:  t.ColorBackground,
		// No bottom padding: the page ends flush with the content's last block,
		// so there is no reserved blank band under the page.
		Padding: gogui.NewPadding(t.PaddingMedium.Top, t.PaddingMedium.Right, 0, t.PaddingMedium.Left),
		Spacing: gogui.SpacingMedium,
		Content: content,
	})
}

// pageContent is the body of the selected page.
func pageContent(t gogui.Theme, app *App, windowWidth int) gogui.View {
	switch app.page {
	case pageSettings:
		return settingsPage(t, app, windowWidth)
	case pageAbout:
		return aboutPage(t)
	default:
		return statusPage(t, app)
	}
}

// navRail is the left navigation: one button per page, the current one filled.
// It has no padding of its own so its width is exactly the button width.
func navRail(t gogui.Theme, app *App) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		ID:         "gui:nav",
		Sizing:     gogui.FixedFit,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Spacing:    gogui.SpacingSmall,
		Content: []gogui.View{
			navButton(t, pageStatus, "Status", app.page),
			navButton(t, pageSettings, "Settings", app.page),
			navButton(t, pageAbout, "About", app.page),
		},
	})
}

// navButton builds one navigation button.  The selected page keeps a filled
// (secondary) face so the current screen reads at a glance; the others stay
// ghost so they recede.  The rail hugs this fixed width, so there is no dead
// space beside the buttons.
func navButton(t gogui.Theme, p pageKind, label string, current pageKind) gogui.View {
	variant := gogui.ButtonGhost
	if p == current {
		variant = gogui.ButtonSecondary
	}

	return gogui.Button(gogui.ButtonCfg{
		ID:      "gui:nav:" + pageName(p),
		Variant: variant,
		Label:   label,
		Width:   navRailWidth,
		Sizing:  gogui.FixedFit,
		OnClick: func(ctx gogui.EventCtx) { setPage(ctx, p) },
	})
}

// navRailWidth is the fixed width of the navigation buttons and the rail.  It
// is wide enough for the longest label ("Settings") plus the theme's button
// inset, so the three labels sit on one aligned column with no clipping.
const navRailWidth = 96

// pageName is the stable suffix for a page's widget ID.
func pageName(p pageKind) (name string) {
	switch p {
	case pageSettings:
		return "settings"
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
		Spacing:    gogui.SpacingMedium,
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
			gogui.Text(gogui.TextCfg{
				Text:      label,
				TextStyle: t.TextStyleLabel,
				MinWidth:  132,
				Sizing:    gogui.FixedFit,
			}),
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
		Spacing:    gogui.SpacingMedium,
		Scrollable: true,
		Content:    content,
	})
}

// header is a single compact row: the app title, a status caption, and the
// window controls.  The single Protection button lives here (labelled Enable
// when interception is off, Disable when on); Restore appears only for the
// crash-leftover state.
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
		gogui.Text(gogui.TextCfg{
			Text:      caption,
			TextStyle: t.TextStyleCaption,
			Sizing:    gogui.FillFit,
			Mode:      gogui.TextModeWrap,
		}),
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

	// The power control is the right-most action: green to enable, red to
	// disable, so the button reads the state, not just its label.
	powerCfg := gogui.ButtonCfg{
		ID:       "gui:power",
		Variant:  gogui.ButtonPrimary,
		Colors:   successButtonColors(t),
		Label:    actionLabel(power),
		Disabled: !ps.canToggle,
		OnClick:  togglePower,
	}
	if ps.on {
		powerCfg.Variant = gogui.ButtonDanger
		powerCfg.Colors = gogui.ColorSet{} // the danger variant's own error fill
	}

	controls = append(controls, gogui.Button(powerCfg))

	return gogui.Row(gogui.ContainerCfg{
		ID:      "gui:header",
		Sizing:  gogui.FillFit,
		Spacing: gogui.SpacingMedium,
		VAlign:  gogui.VAlignMiddle,
		Content: append([]gogui.View{gogui.Text(gogui.TextCfg{
			Text:      "dns-client",
			TextStyle: t.TextStyleTitle,
		})}, controls...),
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
	toastWidth  float32 = 300
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
		SizeBorder:   gogui.BorderPx(1),
		Radius:       gogui.RadiusMedium,
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

// banner is the state headline.  The indicator sits in a row with the headline
// so it is vertically centred on the title line, not the block; the sentence
// sits below.  The indicator is a ring, filled and glowing only when protected.
func banner(t gogui.Theme, v statusVisual) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		ID:      "gui:banner",
		Sizing:  gogui.FillFit,
		Color:   v.bg,
		Radius:  gogui.RadiusMedium,
		Padding: t.PaddingMedium,
		Spacing: gogui.SpacingTight,
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

// card is a bordered panel with a bold heading and label/value rows.  It uses
// a plain filled/outlined Column with an in-flow heading Text rather than the
// toolkit's group-box Title: the group-box legend is drawn outside the
// scrollable clip, so scrolled cards painted their legend at the scroll origin
// and overlapped the banner, and its TitleBG struck a line through the text.
func card(t gogui.Theme, id, title string, rows []gogui.View) gogui.View {
	return cardSpaced(t, id, title, gogui.SpacingTight, rows)
}

// cardSpaced is card with an explicit inter-row gap.  The tight default keeps a
// read-only list compact; the Settings editor asks for a larger step so its many
// fields read as separate rows rather than one dense block.
func cardSpaced(t gogui.Theme, id, title string, spacing gogui.Spacing, rows []gogui.View) gogui.View {
	content := make([]gogui.View, 0, len(rows)+1)
	content = append(content, gogui.Text(gogui.TextCfg{
		Text:      title,
		TextStyle: t.TextStyleTitleSmall,
		Sizing:    gogui.FillFit,
	}))
	content = append(content, rows...)

	return gogui.Column(gogui.ContainerCfg{
		ID:          id,
		Sizing:      gogui.FillFit,
		Color:       t.ColorPanel,
		ColorBorder: t.ColorBorder,
		SizeBorder:  gogui.BorderPx(1),
		Radius:      gogui.RadiusMedium,
		Padding:     t.PaddingMedium,
		Spacing:     spacing,
		Content:     content,
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
			gogui.Text(gogui.TextCfg{
				Text:      label,
				TextStyle: t.TextStyleLabel,
				MinWidth:  132,
				Sizing:    gogui.FixedFit,
			}),
			gogui.Text(gogui.TextCfg{
				Text:      value,
				TextStyle: t.TextStyleBody,
				Sizing:    gogui.FillFit,
				Mode:      gogui.TextModeWrap,
			}),
		},
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

		return statusVisual{
			kind:          kindRunning,
			headline:      "Protected",
			detail:        "DNS queries are handled by the dns-client loopback resolver.",
			headlineStyle: t.TextStyleSuccess,
			bg:            t.ColorSuccessSubtle,
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
