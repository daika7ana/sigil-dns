//go:build gui

package gui

import (
	"fmt"
	"slices"
	"strings"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/daika7ana/sigil-dns/internal/providers"
)

// settingsPage is the configuration screen: the service lifecycle, the profile
// list with its management verbs, and the per-profile editor.  Everything
// renders from the GUI-local draft; edits are staged into an elevated request
// by the apply seam.  Editing works while the service is stopped by falling
// back to the cached last-good summary.  windowWidth sizes the dropdown popups
// (see selectFloor).
func settingsPage(t gogui.Theme, app *App, windowWidth int) gogui.View {
	app.ensureDraft()

	floor := selectFloor(windowWidth)

	content := withEditNotice(t, app, nil)

	content = append(content,
		serviceCard(t, app),
		appearanceCard(t, app),
		trayCard(t, app),
		profilesCard(t, app, floor),
		editorCard(t, app, floor),
	)

	return pageColumn("gui:settings", content)
}

// rulesPage is the filter-list and per-domain routing editor.  It renders in
// ONE scrollable column (the same pattern as the Status page) so a long list
// scrolls instead of growing the window.  The two editors are separate cards
// with their own headings; one Save row sits below both, outside the cards,
// because it applies the whole shared profile draft rather than either card
// alone.
func rulesPage(t gogui.Theme, app *App, windowWidth int) gogui.View {
	app.ensureDraft()
	disabled := app.busy || !canEdit(app.snap) || app.draft.name == ""
	floor := selectFloor(windowWidth)

	// Filtering belongs with the lists it drives: the switch and its block
	// response sit above the rows, not in Settings.
	listsContent := filteringFields(t, app.draft, disabled, floor)
	listsContent = append(listsContent,
		gogui.Separator(gogui.SeparatorCfg{
			ID:    "gui:lists:filtering:divider",
			Color: t.ColorBorder,
		}),
		listSection(t, app, app.draft, disabled),
	)

	content := withEditNotice(t, app, nil)
	content = append(content,
		// The page has no separate title bar, so this quiet context line names
		// the profile the two editors below are writing to.
		gogui.Row(gogui.ContainerCfg{
			ID:         "gui:rules:intro",
			Sizing:     gogui.FillFit,
			Spacing:    gogui.SpacingSmall,
			Padding:    gogui.PaddingNone,
			SizeBorder: gogui.NoBorder,
			VAlign:     gogui.VAlignMiddle,
			Content: []gogui.View{
				gogui.Text(gogui.TextCfg{
					Text:      "Editing profile",
					TextStyle: t.TextStyleCaption,
					Sizing:    gogui.FitFit,
				}),
				chip(t, fallback(app.draft.name, "—")),
			},
		}),
		card(t, "gui:card:lists", "Filter lists", listsContent),
		card(t, "gui:card:rules", "Per-domain routing", []gogui.View{
			ruleSection(t, app.draft, disabled),
		}),
		saveRow(app.draft, disabled),
	)

	return pageColumn("gui:rules", content)
}

// editNotice is the trust/offline banner shown above an editor page (Settings
// and Rules), or nil when neither applies.  Editing is refused while the
// service identity is unverified; the offline hint explains a cached config.
func editNotice(t gogui.Theme, app *App) gogui.View {
	if !canEdit(app.snap) {
		return notice(t, noticeWarning,
			"Service identity not verified",
			"Editing is unavailable until the Sigil DNS service is verified.")
	}
	if hint := editingHint(app); hint != "" {
		return notice(t, noticeInfo, "Offline editing", hint)
	}

	return nil
}

// editingHint explains the offline-editing situation, or "" when the service is
// providing the config.
func editingHint(app *App) (hint string) {
	if app.snap.running {
		return ""
	}
	if app.summary() != nil {
		return "The service is not running, so this form shows the saved configuration. Saving sends an empty revision and does not start the service."
	}

	return "No configuration file yet. Saving creates one from these defaults."
}

// ensureDraft seeds the editor buffer and profile list from the first summary
// (live or cached).  On a brand-new install, where neither exists, it seeds the
// built-in default profile so the editor is usable immediately.  It runs on the
// main thread and is a no-op once seeded, so it never clobbers in-progress
// edits.
func (a *App) ensureDraft() {
	if a.seeded {
		return
	}

	sum := a.summary()
	if sum == nil {
		a.seedDefault()
		return
	}

	if a.active == "" {
		a.active = activeProfileName(sum)
	}
	if a.selected == "" {
		a.selected = a.active
	}
	a.draft = draftFromSummary(sum, a.selected)
	if len(a.profiles) == 0 && a.active != "" {
		a.profiles = []string{a.active}
	}
	a.seeded = true
}

// seedDefault presents and pre-selects the built-in default profile on a fresh
// install (no live config and no cache), so Save can create the config.
func (a *App) seedDefault() {
	d := defaultDraft()
	a.profiles = []string{d.name}
	a.active = d.name
	a.selected = d.name
	a.draft = d
	a.seeded = true
}

// seed folds a fresh poll into the App.  The profile list and the active name
// are refreshed every poll (they are not user-edited state); the editor buffer
// is seeded only once so a poll never clobbers in-progress edits.
func (a *App) seed(s snapshot) {
	a.snap = s
	a.refreshProfiles()
	a.ensureDraft()
}

// refreshProfiles updates the profile list and the active name from the
// effective summary.  If the selected profile vanished (renamed or deleted
// elsewhere), the editor falls back to the active profile.
func (a *App) refreshProfiles() {
	sum := a.summary()
	if sum == nil {
		return
	}

	names := profileNames(sum.Profiles)
	if len(names) > 0 {
		a.profiles = names
	}
	if active := activeProfileName(sum); active != "" {
		a.active = active
	}

	if a.selected != "" && len(names) > 0 && !slices.Contains(names, a.selected) {
		a.selected = a.active
		a.draft = draftFromSummary(sum, a.selected)
		a.seeded = true
	}
}

// serviceCard is the service lifecycle: a single Start/Stop control, then the
// one-time Install/Uninstall.  The Start/Stop choice follows the INTERCEPTION
// state, not the service process: Start runs Enable (which starts the service
// and turns protection on, no-opping the install when it is already running),
// and Stop stops the service — protection goes off with it, but the service
// stays installed.  Keying on interception means a running-but-off state (a
// failed Enable, a `sigildns disable`, or a degraded boot) still offers Start
// so the user can turn protection back on.  The SCM state badge lives in the
// header beside the app name.  Writes are gated by the unverified-trust rule and
// the busy flag.
func serviceCard(t gogui.Theme, app *App) gogui.View {
	disabled := app.busy || !canEdit(app.snap)

	controls := []gogui.View{}
	if app.snap.installed {
		// One Start/Stop control, wearing the palette the old header power
		// button did: the actionable Start is the success green, Stop is a calm
		// neutral, and either reads inert while a write is not allowed.  Start
		// only shows for an installed service — a control that cannot work is
		// not offered.
		action := actionEnable
		label := "Start"
		id := "gui:service:start"
		if enabled(app.snap) {
			action = actionServiceStop
			label = "Stop"
			id = "gui:service:stop"
		}

		startCfg := gogui.ButtonCfg{
			ID: id, Variant: gogui.ButtonSecondary, Label: label,
			Disabled: disabled,
			OnClick:  func(ctx gogui.EventCtx) { runAction(ctx, action) },
		}
		switch {
		case disabled:
			startCfg.Colors = disabledNeutralColors(t)
		case action == actionEnable:
			startCfg.Variant = gogui.ButtonPrimary
			startCfg.Colors = successButtonColors(t)
		}
		controls = append(controls, gogui.Button(startCfg))
	}

	if app.snap.installed {
		// Uninstall is consequential but it is not this card's primary action,
		// so it wears the shared subordinate danger outline rather than a
		// filled red block beside the calm Stop control.
		controls = append(controls, dangerOutlineButton(t,
			"gui:service:uninstall", actionLabel(actionUninstall)+" service",
			disabled, confirmUninstall))
	} else {
		controls = append(controls, gogui.Button(gogui.ButtonCfg{
			ID:       "gui:service:install",
			Variant:  actionVariant(actionInstall),
			Label:    actionLabel(actionInstall) + " service",
			Disabled: disabled,
			OnClick:  func(ctx gogui.EventCtx) { runAction(ctx, actionInstall) },
		}))
	}

	// The header badge carries the installed/not-installed state, so there is no
	// separate "Installed" row: one source of truth for the service state.
	return card(t, "gui:card:service", "Service", []gogui.View{
		settingRow(t, "Background service",
			"Runs the local resolver and applies the system DNS settings.",
			gogui.Row(gogui.ContainerCfg{
				Sizing:     gogui.FitFit,
				Spacing:    gogui.SpacingSmall,
				Padding:    gogui.PaddingNone,
				SizeBorder: gogui.NoBorder,
				VAlign:     gogui.VAlignMiddle,
				Content:    controls,
			})),
	})
}

// appearanceCard holds the app-level look: the Auto/Light/Dark picker that
// used to sit in the header.  It writes through setThemePref, so the choice
// still persists to gui.json.
func appearanceCard(t gogui.Theme, app *App) gogui.View {
	return card(t, "gui:card:appearance", "Appearance", []gogui.View{
		settingRow(t, "Theme",
			"Follow Windows, or keep the window light or dark.",
			gogui.SegmentedControl(gogui.SegmentedControlCfg{
				ID:      "gui:theme",
				Value:   app.themePref,
				Sizing:  gogui.FitFit,
				Options: themeOptions(),
				OnSelect: func(v string, ctx gogui.EventCtx) {
					setThemePref(ctx, v)
				},
			})),
	})
}

// confirmUninstall asks before removing the service.  Removing it stops
// protection and deletes the service registration, so it confirms first.
func confirmUninstall(ctx gogui.EventCtx) {
	ctx.Window.Dialog(gogui.DialogCfg{
		DialogType: gogui.DialogConfirm,
		Title:      "Uninstall service",
		Body:       "Removing the service stops protection and deletes the service registration.",
		FocusID:    "gui:service:uninstall:confirm",
		OnOkYes:    func(w *gogui.Window) { runAction(gogui.EventCtx{Window: w}, actionUninstall) },
	})
}

// serviceBadge is the SCM state as a Fluent status pill.  It carries the
// capitalized state word (the DTO keeps the lowercase value).
func serviceBadge(s snapshot) gogui.View {
	variant := gogui.BadgeSuccess
	switch {
	case !s.installed:
		variant = gogui.BadgeInfo
	case !s.running:
		variant = gogui.BadgeWarning
	}

	return gogui.Badge(gogui.BadgeCfg{
		Label:   displayState(s.scmState),
		Variant: variant,
	})
}

// selectFloor is the shared width floor for every Settings dropdown.  The
// pinned toolkit sizes an open popup from MinWidth/MaxWidth plus its own
// content and has no "fill to the field" lever; MinWidth is also applied to the
// closed field, but only as a floor, so a value below the field's own width
// leaves the filling field untouched while lifting every popup close to it.
//
// It mirrors the content column's geometry: the contentArea inset, the page
// column's own padding (with its scrollbar gutter), and the card inset.  The
// column is capped at contentMaxWidth, so the floor tracks the window until the
// cap binds.  The ratio keeps it just under the field to avoid ever growing the
// field past its card.  Long-content lists (Provider, Variant) may still exceed
// the floor — the toolkit gives no way to cap the popup without also capping
// the field.
func selectFloor(windowWidth int) float32 {
	t := gogui.CurrentTheme()
	const (
		ratio    float32 = 0.92
		minFloor float32 = 200
	)

	navTotal := navRailWidth + 2*navRailPad

	// Space the centered column may occupy, then the cap.
	avail := float32(windowWidth) - navTotal - t.SizeBorder - 2*contentPad
	pageCol := min(avail, contentMaxWidth)

	// Inside the column: the page padding (contentPad + scrollbarGutter across)
	// and the card's own inset.
	pageInner := pageCol - (2*contentPad + scrollbarGutter)
	cardInner := pageInner - t.PaddingLarge.Width() - 2*t.SizeBorder

	floor := cardInner * ratio
	if floor < minFloor {
		floor = minFloor
	}

	return floor
}

// formSelect builds a Select styled for the Settings form: it fills the card
// column, and the open popup shares the common width floor so the dropdowns
// read as consistent menus rather than content-sized boxes.  It is the one
// place the Settings selects are constructed, so the styling cannot drift
// between them.
func formSelect(cfg gogui.SelectCfg, floor float32) gogui.View {
	cfg.Sizing = gogui.FillFit
	cfg.MinWidth = floor

	return gogui.Select(cfg)
}

// profilesCard is the profile selector plus the Add / Rename / Delete verbs.
func profilesCard(t gogui.Theme, app *App, floor float32) gogui.View {
	names := profilesFor(app)
	selected := selectedProfile(app)
	disabled := app.busy || !canEdit(app.snap)

	return card(t, "gui:card:profiles", "Profiles", []gogui.View{
		formSelect(gogui.SelectCfg{
			ID:       "gui:profile:select",
			Label:    "Profile",
			Items:    names,
			Selected: one(selected),
			Disabled: disabled || len(names) == 0,
			OnSelect: func(vals []string, ctx gogui.EventCtx) { setSelectedProfile(ctx, vals) },
		}, floor),
		caption(t, "The active profile handles every query. Select a profile here, then edit it in the Profile editor."),
		gogui.Row(gogui.ContainerCfg{
			Sizing:     gogui.FillFit,
			Spacing:    gogui.SpacingSmall,
			Padding:    gogui.PaddingNone,
			SizeBorder: gogui.NoBorder,
			VAlign:     gogui.VAlignMiddle,
			Content: []gogui.View{
				gogui.Button(gogui.ButtonCfg{
					ID: "gui:profile:add", Variant: gogui.ButtonSecondary, Label: "Add",
					Sizing: gogui.FillFit, Disabled: disabled, OnClick: promptAddProfile,
				}),
				gogui.Button(gogui.ButtonCfg{
					ID: "gui:profile:rename", Variant: gogui.ButtonSecondary, Label: "Rename",
					Sizing: gogui.FillFit, Disabled: disabled || selected == "", OnClick: promptRenameProfile,
				}),
				gogui.Button(gogui.ButtonCfg{
					ID: "gui:profile:active", Variant: gogui.ButtonSecondary, Label: "Set active",
					Sizing:   gogui.FillFit,
					Disabled: disabled || selected == "" || selected == app.active,
					OnClick: func(ctx gogui.EventCtx) {
						runSetActive(ctx.Window, selectedProfile(app))
					},
				}),
				// Destructive and irreversible: the subordinate outline-danger
				// treatment, so it does not out-shout the neutral verbs.
				dangerOutlineButton(t, "gui:profile:delete", "Delete",
					disabled || deleteDisabled(names, app.active, selected), confirmDeleteProfile),
			},
		}),
	})
}

// dangerOutlineColors is the subordinate destructive look for an ENABLED row
// action: a control-fill button whose 1px error outline and error label give it
// the same silhouette as its neutral siblings, rather than a filled red block
// or bare red text.
func dangerOutlineColors(t gogui.Theme) gogui.ColorSet {
	return gogui.ColorSet{
		Base:     t.ColorInterior,
		Hover:    t.ColorErrorSubtle,
		Click:    t.ColorErrorSubtle,
		Focus:    t.ColorInterior,
		Border:   t.Cfg.ColorError,
		Disabled: t.Cfg.ColorActive,
	}
}

// dangerOutlineDisabledColors is the same button when it cannot run: a neutral
// fill and a neutral border that still draw the button silhouette (matching its
// disabled siblings), so an inert destructive control reads muted rather than
// as bare red text.  Disabled is pinned so the renderer paints the muted fill
// as given instead of a dimmed danger base.
func dangerOutlineDisabledColors(t gogui.Theme) gogui.ColorSet {
	return gogui.ColorSet{
		Base:     t.Cfg.ColorActive,
		Hover:    t.Cfg.ColorActive,
		Click:    t.Cfg.ColorActive,
		Focus:    t.Cfg.ColorActive,
		Border:   t.Cfg.ColorBorder,
		Disabled: t.Cfg.ColorActive,
	}
}

// dangerOutlineButton is the one treatment for a destructive action that sits
// inside a card (profile Delete, list/rule Remove, service Uninstall): a
// danger-styled outline that stays subordinate on an otherwise-neutral form.
// Enabled it is an error outline with an error label; disabled it keeps the
// same button shape but goes fully neutral and inert.
func dangerOutlineButton(t gogui.Theme, id, label string, disabled bool, onClick func(gogui.EventCtx)) gogui.View {
	style := t.TextStyleError
	colors := dangerOutlineColors(t)
	if disabled {
		style = t.TextStyleDisabled
		colors = dangerOutlineDisabledColors(t)
	}

	return gogui.Button(gogui.ButtonCfg{
		ID: id, Variant: gogui.ButtonSecondary, Sizing: gogui.FitFit,
		Colors: colors, Disabled: disabled,
		Content: []gogui.View{gogui.Text(gogui.TextCfg{
			Text: label, TextStyle: style, Sizing: gogui.FitFit,
		})},
		OnClick: onClick,
	})
}

// editorCard is the per-profile editor for the selected profile.  It is
// editable whenever there is a profile to edit — including the built-in default
// seeded on a fresh install — and the identity check passes.  The fields are
// grouped into headed sections so the dense form reads as a setting page rather
// than one long block; the cache switch is a settings row, like Window & tray.
func editorCard(t gogui.Theme, app *App, floor float32) gogui.View {
	d := app.draft
	disabled := app.busy || !canEdit(app.snap) || d.name == ""

	rows := []gogui.View{
		fieldGroup(t, "gui:edit:group:provider", "Provider", providerFields(t, d, disabled, floor)...),
		groupDivider(t, 1),
		fieldGroup(t, "gui:edit:group:failover", "Failover", fallbackFields(t, app, d, disabled, floor)...),
		groupDivider(t, 2),
	}
	rows = append(rows, cacheFields(t, d, disabled)...)
	rows = append(rows, saveRow(d, disabled))

	return card(t, "gui:card:editor", "Profile editor", rows)
}

// providerFields is the provider/variant/transport/identity block of the editor.
func providerFields(t gogui.Theme, d profileDraft, disabled bool, floor float32) []gogui.View {
	fields := []gogui.View{
		formSelect(gogui.SelectCfg{
			ID: "gui:edit:provider",
			// The group heading above already reads "Provider", so this field
			// carries no visible label; it is named for assistive tech only,
			// the same way the rule-row upstream input is.
			A11YCfg: gogui.A11YCfg{A11YLabel: "Provider"},
			Options: providerOptions(), Selected: one(d.provider),
			Disabled: disabled, OnSelect: setDraftProvider,
		}, floor),
	}
	// The provider's qualification belongs under the choice, not inside its
	// label: the option itself stays the provider's human name.
	if detail := providerDetail(d.provider, d.variant); detail != "" {
		fields = append(fields, captionID(t, "gui:edit:provider:detail", detail))
	}
	fields = append(fields, formSelect(gogui.SelectCfg{
		ID: "gui:edit:variant", Label: "Variant",
		Options: variantOptions(d.provider), Selected: one(d.variant),
		Disabled: disabled,
		OnSelect: func(vals []string, ctx gogui.EventCtx) {
			updateDraft(ctx, func(d *profileDraft) {
				d.variant = first(vals)
				syncBootstrap(d)
			})
		},
	}, floor))

	// The custom provider's transport is dictated by its URL, so there is no
	// protocol choice to make; show a one-line hint instead of the control.
	if d.provider == providers.Custom {
		fields = append(fields, caption(t, "Uses the protocol in the custom URL."))
	} else {
		fields = append(fields, protocolSelect(d, disabled, floor))
	}

	// The resolver id is only meaningful for providers that use one; the
	// field stays visible but inert otherwise, with a line explaining why and,
	// when it is live, what to enter.  The state is derived per frame from the
	// current provider+variant, so switching either updates it on the next
	// repaint.
	idNeeded := needsResolverID(d.provider, d.variant)

	fields = append(fields, gogui.Input(gogui.InputCfg{
		ID: "gui:edit:id", Label: "Resolver ID", Sizing: gogui.FillFit,
		Text: d.id, IsPassword: true, Placeholder: "Leave blank to keep",
		Disabled: disabled || !idNeeded,
		OnTextChanged: func(text string, ctx gogui.EventCtx) {
			updateDraft(ctx, func(d *profileDraft) {
				d.id = text
				syncBootstrap(d)
			})
		},
	}))

	// One line under the field, scoped to the provider and variant: the
	// instruction the user needs for this choice, or the reason the field is
	// inert.  It replaces the CLI-style "--id" examples the catalog carries.
	fields = append(fields, captionID(t, "gui:edit:id:hint", resolverIDHint(d.provider, d.variant)))

	if d.provider == providers.Custom {
		fields = append(fields, gogui.Input(gogui.InputCfg{
			ID: "gui:edit:url", Label: "Custom URL", Sizing: gogui.FillFit,
			Text: d.url, Placeholder: "leave blank to keep", Disabled: disabled,
			OnTextChanged: func(text string, ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) { d.url = text })
			},
		}))
	}

	return fields
}

// fallbackFields is the fallback-profile selector and its explanation.
func fallbackFields(t gogui.Theme, app *App, d profileDraft, disabled bool, floor float32) []gogui.View {
	// The fallback is another profile, never this one, so the sentinel ("")
	// is a real selection rather than "nothing chosen" — hence not one().
	options := []gogui.SelectOption{gogui.NewSelectOption("No fallback", "")}
	for _, name := range profilesFor(app) {
		if name != d.name {
			options = append(options, gogui.NewSelectOption(name, name))
		}
	}

	return []gogui.View{
		formSelect(gogui.SelectCfg{
			ID: "gui:edit:fallback", Label: "Fallback profile",
			Options: options, Selected: []string{d.fallback},
			Disabled: disabled,
			OnSelect: func(vals []string, ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) { d.fallback = first(vals) })
			},
		}, floor),
		caption(t, "Used when this profile's DNS servers time out or fail. It uses the fallback profile's servers and bootstrap addresses."),
	}
}

// filteringFields is the filtering toggle and its block-response selector.  The
// toggle is a settings row — its title and descriptor on the left, the switch on
// the right — matching the Window & tray switches.
func filteringFields(t gogui.Theme, d profileDraft, disabled bool, floor float32) []gogui.View {
	return []gogui.View{
		settingRow(t, "Filtering",
			"Blocks domains on the enabled filter lists below.",
			gogui.Switch(gogui.SwitchCfg{
				ID:       "gui:edit:filtering",
				A11YCfg:  gogui.A11YCfg{A11YLabel: "Filtering"},
				Selected: d.filtering,
				Disabled: disabled,
				OnClick: func(ctx gogui.EventCtx) {
					updateDraft(ctx, func(d *profileDraft) { d.filtering = !d.filtering })
				},
			})),
		formSelect(gogui.SelectCfg{
			ID: "gui:edit:block", Label: "Block response",
			Options: blockResponseOptions(), Selected: one(d.blockResp),
			Disabled: disabled || !d.filtering,
			OnSelect: func(vals []string, ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) { d.blockResp = first(vals) })
			},
		}, floor),
		caption(t, "The answer sent for blocked domains."),
	}
}

// cacheFields is the cache toggle, size input, and bootstrap input.  The toggle
// is a settings row — its title and descriptor on the left, the switch on the
// right — matching the Window & tray switches.
func cacheFields(t gogui.Theme, d profileDraft, disabled bool) []gogui.View {
	return []gogui.View{
		settingRow(t, "Cache",
			"Keeps recent answers so repeat lookups are faster.",
			gogui.Switch(gogui.SwitchCfg{
				ID:       "gui:edit:cache",
				A11YCfg:  gogui.A11YCfg{A11YLabel: "Cache"},
				Selected: d.cache,
				Disabled: disabled,
				OnClick: func(ctx gogui.EventCtx) {
					updateDraft(ctx, func(d *profileDraft) { d.cache = !d.cache })
				},
			})),
		gogui.Input(gogui.InputCfg{
			ID: "gui:edit:cachesize", Label: "Cache size, MiB",
			Sizing: gogui.FillFit,
			Text:   d.cacheSizeMB, Disabled: disabled || !d.cache,
			OnTextChanged: func(text string, ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) { d.cacheSizeMB = text })
			},
		}),
		bootstrapField(t, d, disabled),
	}
}

// groupDivider separates two field groups in the editor card, so the grouping
// stays legible even where a heading's text is not read.
func groupDivider(t gogui.Theme, i int) gogui.View {
	return gogui.Separator(gogui.SeparatorCfg{
		ID:    fmt.Sprintf("gui:edit:divider:%d", i),
		Color: t.ColorBorder,
	})
}

// bootstrapField is the bootstrap server list input.
func bootstrapField(t gogui.Theme, d profileDraft, disabled bool) gogui.View {
	return gogui.Input(gogui.InputCfg{
		ID: "gui:edit:bootstrap", Label: "Bootstrap servers",
		Sizing: gogui.FillFit,
		Text:   d.bootstrap, Disabled: disabled,
		Placeholder: "1.1.1.1, 8.8.8.8",
		OnTextChanged: func(text string, ctx gogui.EventCtx) {
			updateDraft(ctx, func(d *profileDraft) { d.bootstrap = text })
		},
	})
}

// fieldGroup is a headed block of related fields inside the editor card.  The
// heading is a quiet, subordinate level below the card title (the card title is
// the strong "body strong" heading; a group heading is smaller and quieter), so
// the hierarchy reads: card title > group heading > field.  The fields keep one
// uniform tight rhythm, and the card's dividers mark the boundaries.
func fieldGroup(t gogui.Theme, id, title string, fields ...gogui.View) gogui.View {
	content := []gogui.View{gogui.Text(gogui.TextCfg{
		ID:        id,
		Text:      title,
		TextStyle: t.TextStyleLabel,
		Sizing:    gogui.FillFit,
	})}
	content = append(content, fields...)

	return gogui.Column(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		Spacing:    gogui.SpacingSmall,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content:    content,
	})
}

// saveButtonMinWidth is the comfortable minimum for the primary Save action.
// A Fit-width button would collapse to its label in a font-less render; a floor
// keeps it a button-sized target.
const saveButtonMinWidth float32 = 140

// saveRow is the Save button, right-aligned to the field column.  Save is a
// content-width button so the primary action reads as a button rather than a
// full-column bar; any field annotation belongs beside its field, not here.
func saveRow(d profileDraft, disabled bool) gogui.View {
	return primaryActionRow("gui:profile:save", "Save", disabled,
		func(ctx gogui.EventCtx) { runSaveProfile(ctx, d) })
}

// primaryActionRow is a right-aligned primary action button, optionally with
// content (such as a note) above it in the same column.
func primaryActionRow(id, label string, disabled bool, onClick func(gogui.EventCtx), above ...gogui.View) gogui.View {
	content := append(above, gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		HAlign:     gogui.HAlignEnd,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			gogui.Button(gogui.ButtonCfg{
				ID: id, Variant: gogui.ButtonPrimary, Label: label,
				Sizing: gogui.FitFit, MinWidth: saveButtonMinWidth, Disabled: disabled,
				OnClick: onClick,
			}),
		},
	}))

	return gogui.Column(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		Spacing:    gogui.SpacingTight,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content:    content,
	})
}

// listSection is the filter-list editor: one row per list (an existing row
// shows its redacted host), an add-by-URL field, and the service action that
// re-downloads the active profile's lists.  It is the body of the "Filter
// lists" card, so the card title carries the heading and there is no second
// sub-heading here.
func listSection(t gogui.Theme, app *App, d profileDraft, disabled bool) gogui.View {
	children := []gogui.View{}
	if len(d.lists) == 0 {
		children = append(children, caption(t, "No filter lists yet."))
	}
	for i := range d.lists {
		children = append(children, listRow(t, d, i, disabled))
	}

	// Add-by-URL: paste a URL and Add.  Only a new list carries its URL in
	// full; existing lists are edited by index (SPEC §15.2).  The field and its
	// button are one InputGroup, so the trailing control is attached to the
	// field (no detached gap) and the pair shares the card's right edge.
	children = append(children,
		gogui.InputGroup(gogui.InputGroupCfg{
			ID:     "gui:list:add:row",
			Sizing: gogui.FillFit,
			Segments: []gogui.InputGroupSegment{
				gogui.InputGroupInput(gogui.InputCfg{
					ID: "gui:list:add:url", Sizing: gogui.FillFit,
					Text:        app.listInput,
					Placeholder: "https://example.com/hosts.txt",
					Disabled:    disabled,
					OnTextChanged: func(text string, ctx gogui.EventCtx) {
						setListInput(ctx, text)
					},
				}),
				gogui.InputGroupButton(gogui.ButtonCfg{
					ID: "gui:list:add", Variant: gogui.ButtonSecondary, Label: "Add list",
					Sizing: gogui.FitFit, Disabled: disabled, OnClick: addList,
				}),
			},
		}),
		gogui.Separator(gogui.SeparatorCfg{
			ID:    "gui:lists:divider",
			Color: t.ColorBorder,
		}),
		refreshRow(t, app, disabled),
	)

	return gogui.Column(gogui.ContainerCfg{
		ID: "gui:edit:lists", Sizing: gogui.FillFit,
		Spacing: gogui.SpacingMedium, Padding: gogui.PaddingNone,
		SizeBorder: gogui.NoBorder, Content: children,
	})
}

// listRow is one filter list: its redacted host (or a newly typed URL), an
// enable toggle, and a remove control.  The two controls form one right-hand
// cluster with fixed inter-control spacing, so the toggle and remove columns
// land in the same place in every list and rule row.
func listRow(t gogui.Theme, d profileDraft, i int, disabled bool) gogui.View {
	l := d.lists[i]
	label := l.host
	if l.idx < 0 {
		label = l.url
	}

	return gogui.Row(gogui.ContainerCfg{
		ID: fmt.Sprintf("gui:list:%d", i), Sizing: gogui.FillFit,
		Spacing: gogui.SpacingMedium, Padding: gogui.PaddingNone,
		SizeBorder: gogui.NoBorder, VAlign: gogui.VAlignMiddle,
		Content: []gogui.View{
			gogui.Text(gogui.TextCfg{
				ID: fmt.Sprintf("gui:list:%d:host", i), Text: fallback(label, "(list)"),
				TextStyle: t.TextStyleBody, Sizing: gogui.FillFit, Mode: gogui.TextModeWrap,
			}),
			rowControlCluster(t,
				gogui.Toggle(gogui.ToggleCfg{
					ID: fmt.Sprintf("gui:list:%d:enable", i), Label: "Enabled",
					Selected: l.enabled, Disabled: disabled, OnClick: listToggle(i),
				}),
				dangerOutlineButton(t, fmt.Sprintf("gui:list:%d:remove", i), "Remove",
					disabled, removeList(i)),
			),
		},
	})
}

// rowControlCluster is the shared right-hand control column of a list or rule
// row: an enable toggle and a subordinate remove, at one fixed inter-control
// gap so both controls read as two deliberate columns rather than drift.
func rowControlCluster(t gogui.Theme, toggle, remove gogui.View) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FitFit,
		Spacing:    gogui.SpacingSmall,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		VAlign:     gogui.VAlignMiddle,
		Content:    []gogui.View{toggle, remove},
	})
}

// refreshRow is the "Update filter lists" service action: it re-downloads the
// active profile's lists over the running service.  It carries no config patch,
// so it is offered only while the service is up.  It is a settings row rather
// than a full-width bar, so its button shares the page's right control edge.
func refreshRow(t gogui.Theme, app *App, disabled bool) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		ID: "gui:lists:refresh:row", Sizing: gogui.FillFit,
		Spacing: gogui.NoSpacing, Padding: gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			settingRow(t, "Filter list updates",
				"Downloads the active profile's lists again. The service must be running.",
				gogui.Button(gogui.ButtonCfg{
					ID: "gui:lists:refresh", Variant: gogui.ButtonSecondary,
					Label: "Update filter lists", Sizing: gogui.FitFit,
					Disabled: disabled || !app.snap.running,
					OnClick:  func(ctx gogui.EventCtx) { runAction(ctx, actionRefreshLists) },
				})),
		},
	})
}

// The two per-domain rule kinds, as the SegmentedControl values.
const (
	ruleKindUpstream = "upstream"
	ruleKindBypass   = "bypass"
)

// ruleSection is the per-domain routing editor: comma-separated domains, a
// rule-kind choice (upstream or bypass), and — for an upstream rule — its
// target, with an enable toggle, add/remove controls, and a one-line note on
// how wildcard patterns match.  A blank upstream keeps the existing rule's
// target (the stored one is never shown).  It is the body of the "Per-domain
// routing" card, so the card title carries the heading.
func ruleSection(t gogui.Theme, d profileDraft, disabled bool) gogui.View {
	children := []gogui.View{
		caption(t, "*.example.com matches subdomains only; example.com matches the domain and its subdomains."),
	}
	if len(d.perDomain) == 0 {
		children = append(children, caption(t, "No per-domain rules."))
	}
	for i := range d.perDomain {
		children = append(children, ruleRow(t, d, i, disabled))
	}
	children = append(children, gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		HAlign:     gogui.HAlignEnd,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			gogui.Button(gogui.ButtonCfg{
				ID: "gui:rule:add", Variant: gogui.ButtonSecondary, Label: "Add rule",
				Sizing: gogui.FitFit, Disabled: disabled || addRuleDisabled(d), OnClick: addRule,
			}),
		},
	}))

	return gogui.Column(gogui.ContainerCfg{
		ID: "gui:edit:rules", Sizing: gogui.FillFit,
		Spacing: gogui.SpacingMedium, Padding: gogui.PaddingNone,
		SizeBorder: gogui.NoBorder, Content: children,
	})
}

// addRuleDisabled reports whether "Add rule" must be inert: while any existing
// rule still has blank domains, adding another would only stack another empty
// row.  It reads the live draft, so filling the last blank row re-enables the
// button on the next repaint (every row handler already requests one).  A blank
// rule set leaves it enabled, as does a set whose every rule has domains.
func addRuleDisabled(d profileDraft) (disabled bool) {
	for _, r := range d.perDomain {
		if strings.TrimSpace(r.domains) == "" {
			return true
		}
	}

	return false
}

// ruleRow is one per-domain rule, drawn as its own contained panel: a numbered
// header over a hairline rule, then the domain list, a rule-kind selector
// (Upstream / Bypass), the kind's target, an enable toggle, and a remove
// control.  The panel and the numbering are what keep a stack of rules legible
// — otherwise identical empty rows read as one undifferentiated column.  An
// upstream row shows the target input (blank means "keep"; the stored target is
// never seeded or shown); a bypass row replaces it with a plain caption,
// because the domains resolve via the system resolver and there is no upstream
// to type.
//
// The kind control is its own row: with only two options, the selected segment
// names the target, so the "Rule type" and "Upstream" text labels the first cut
// carried are gone.  The upstream input keeps the name "Upstream" for assistive
// tech through its A11Y label rather than a visible label — a labelled Input
// wraps label+field in a Column, so centring the toggle and remove against that
// whole stack (VAlignMiddle) would leave them high of the field's centreline.
func ruleRow(t gogui.Theme, d profileDraft, i int, disabled bool) gogui.View {
	r := d.perDomain[i]

	kind := ruleKindUpstream
	if r.bypass {
		kind = ruleKindBypass
	}

	// The target block follows the kind: an input for an upstream rule, a
	// caption for a bypass rule.  Either way the enable/remove cluster sits
	// beside it, so both controls keep working for both kinds and stay on the
	// panel's inner right edge.
	var target gogui.View
	if r.bypass {
		target = gogui.Row(gogui.ContainerCfg{
			Sizing: gogui.FillFit, Spacing: gogui.SpacingMedium,
			Padding: gogui.PaddingNone, SizeBorder: gogui.NoBorder,
			VAlign: gogui.VAlignMiddle,
			Content: []gogui.View{
				caption(t, "Resolved by the system's normal DNS, not by Sigil DNS."),
				ruleControls(t, i, r.enabled, disabled),
			},
		})
	} else {
		target = gogui.Row(gogui.ContainerCfg{
			Sizing: gogui.FillFit, Spacing: gogui.SpacingMedium,
			Padding: gogui.PaddingNone, SizeBorder: gogui.NoBorder,
			VAlign: gogui.VAlignMiddle,
			Content: []gogui.View{
				gogui.Input(gogui.InputCfg{
					ID:     fmt.Sprintf("gui:rule:%d:upstream", i),
					Sizing: gogui.FillFit, Text: r.upstream,
					Placeholder: "leave blank to keep", Disabled: disabled,
					A11YCfg:       gogui.A11YCfg{A11YLabel: "Upstream"},
					OnTextChanged: func(text string, ctx gogui.EventCtx) { setRuleUpstream(i, text, ctx) },
				}),
				ruleControls(t, i, r.enabled, disabled),
			},
		})
	}

	return gogui.Column(gogui.ContainerCfg{
		ID:          fmt.Sprintf("gui:rule:%d", i),
		Sizing:      gogui.FillFit,
		Color:       t.ColorInterior,
		ColorBorder: t.ColorBorder,
		SizeBorder:  gogui.BorderThin,
		Radius:      gogui.RadiusMedium,
		Padding:     t.PaddingMedium,
		Spacing:     gogui.SpacingSmall,
		Content: []gogui.View{
			ruleTitle(t, i),
			gogui.Separator(gogui.SeparatorCfg{
				ID:    fmt.Sprintf("gui:rule:%d:sep", i),
				Color: t.ColorBorder,
			}),
			gogui.Input(gogui.InputCfg{
				ID: fmt.Sprintf("gui:rule:%d:domains", i), Label: "Domains",
				Sizing: gogui.FillFit, Text: r.domains,
				Placeholder: "example.com, *.example.com", Disabled: disabled,
				OnTextChanged: func(text string, ctx gogui.EventCtx) { setRuleDomains(i, text, ctx) },
			}),
			gogui.SegmentedControl(gogui.SegmentedControlCfg{
				ID:     fmt.Sprintf("gui:rule:%d:kind", i),
				Sizing: gogui.FillFit, Value: kind, Disabled: disabled,
				Options: []gogui.SegmentOption{
					gogui.NewSegmentOption("Upstream", ruleKindUpstream),
					gogui.NewSegmentOption("Bypass", ruleKindBypass),
				},
				OnSelect: func(v string, ctx gogui.EventCtx) { setRuleType(i, v, ctx) },
			}),
			target,
		},
	})
}

// ruleTitle is a rule panel's numbered header ("Rule 1"), so a stack of rules
// is countable and each unit is named at a glance.
func ruleTitle(t gogui.Theme, i int) gogui.View {
	return gogui.Text(gogui.TextCfg{
		ID:        fmt.Sprintf("gui:rule:%d:title", i),
		Text:      fmt.Sprintf("Rule %d", i+1),
		TextStyle: t.TextStyleLabel,
		Sizing:    gogui.FillFit,
	})
}

// ruleControls is the shared right-hand cluster of a rule row — an enable
// toggle and a subordinate remove — identical for both rule kinds.
func ruleControls(t gogui.Theme, i int, enabled, disabled bool) gogui.View {
	return rowControlCluster(t,
		gogui.Toggle(gogui.ToggleCfg{
			ID: fmt.Sprintf("gui:rule:%d:enable", i), Label: "Enabled",
			Selected: enabled, Disabled: disabled, OnClick: ruleToggle(i),
		}),
		dangerOutlineButton(t, fmt.Sprintf("gui:rule:%d:remove", i), "Remove",
			disabled, removeRule(i)),
	)
}

// notice is a calm inline banner for a blocking condition.  It is a neutral
// panel with a thin leading severity stripe — not a full-bleed wash — so it
// never out-shouts the page.  The title is a real heading; the detail line is
// secondary.  The stripe's hue is the only difference between the two
// severities (a warning caution vs an informational note).
func notice(t gogui.Theme, kind noticeKind, title, detail string) gogui.View {
	lines := []gogui.View{
		gogui.Text(gogui.TextCfg{
			Text:      title,
			TextStyle: t.TextStyleTitleSmall,
			Sizing:    gogui.FillFit,
		}),
	}
	// A detail that only repeats the title is dropped, not rendered as filler.
	if detail := alertBody(title, detail); detail != "" {
		lines = append(lines, gogui.Text(gogui.TextCfg{
			Text:      detail,
			TextStyle: t.TextStyleSecondary,
			Mode:      gogui.TextModeWrap,
			Sizing:    gogui.FillFit,
		}))
	}

	return gogui.Row(gogui.ContainerCfg{
		ID:          "gui:notice",
		Sizing:      gogui.FillFit,
		Color:       t.ColorPanel,
		ColorBorder: t.ColorBorder,
		SizeBorder:  gogui.BorderThin,
		Radius:      gogui.RadiusLarge,
		Padding:     t.PaddingMedium,
		Spacing:     gogui.SpacingMedium,
		VAlign:      gogui.VAlignMiddle,
		Content: []gogui.View{
			// The severity stripe: a 3px rounded bar spanning the notice height.
			severityStripe(t, "", kind),
			gogui.Column(gogui.ContainerCfg{
				Sizing:     gogui.FillFill,
				Spacing:    gogui.SpacingTight,
				Padding:    gogui.PaddingNone,
				SizeBorder: gogui.NoBorder,
				Content:    lines,
			}),
		},
	})
}

// noticeAccent is the leading stripe color for an inline notice: a warning hue
// for a caution (unverified identity), the accent for an informational note
// (offline editing).  Both are calm accents, not fills.
func noticeAccent(t gogui.Theme, kind noticeKind) gogui.Color {
	if kind == noticeWarning {
		return t.TextStyleWarning.Color
	}

	return t.TextStyleLink.Color
}

// severityStripe is the shared 3px rounded leading bar for a notice or note.
func severityStripe(t gogui.Theme, id string, kind noticeKind) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		ID:         id,
		Width:      3,
		Sizing:     gogui.FixedFill,
		Color:      noticeAccent(t, kind),
		Radius:     gogui.RadiusPx(2),
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
	})
}

// captionStyle is the caption rung in the muted secondary colour.  The toolkit's
// caption token is darker than its secondary one, which made helper text louder
// than the labels it explains.
func captionStyle(t gogui.Theme) gogui.TextStyle {
	style := t.TextStyleCaption
	style.Color = t.TextStyleSecondary.Color

	return style
}

// caption is a small, wrapped secondary line under a field.
func caption(t gogui.Theme, s string) gogui.View {
	return captionID(t, "", s)
}

// captionID is caption with a widget ID, for a line that changes with the form
// state and needs to be found in the layout tree.
func captionID(t gogui.Theme, id, s string) gogui.View {
	return gogui.Text(gogui.TextCfg{
		ID:        id,
		Text:      s,
		TextStyle: captionStyle(t),
		Mode:      gogui.TextModeWrap,
		Sizing:    gogui.FillFit,
	})
}

// --- draft mutation handlers (main thread) ---

// updateDraft mutates the editor buffer and requests a repaint.
func updateDraft(ctx gogui.EventCtx, fn func(*profileDraft)) {
	app := gogui.State[App](ctx.Window)
	fn(&app.draft)
	app.seeded = true
	ctx.Window.InvalidateLayout()
}

// setDraftProvider switches the provider and resets the variant to the new
// provider's default, so the two can never disagree.
func setDraftProvider(vals []string, ctx gogui.EventCtx) {
	name := first(vals)
	updateDraft(ctx, func(d *profileDraft) {
		d.provider = name
		d.variant = defaultVariant(name)
		syncBootstrap(d)
	})
}

// setSelectedProfile switches which profile the editor shows, seeding the
// buffer from that profile's sanitized summary.
func setSelectedProfile(ctx gogui.EventCtx, vals []string) {
	app := gogui.State[App](ctx.Window)
	name := first(vals)
	app.selected = name
	app.draft = draftFromSummary(app.summary(), name)
	app.listInput = ""
	app.seeded = true
	ctx.Window.InvalidateLayout()
}

// --- list + rule editor handlers (main thread) ---

// setListInput buffers the not-yet-added list URL.  It is UI state only: no
// config value is ever read into it.
func setListInput(ctx gogui.EventCtx, text string) {
	app := gogui.State[App](ctx.Window)
	app.listInput = text
	ctx.Window.InvalidateLayout()
}

// addList appends the buffered URL as a new draft list row (enabled), then
// clears the field.  A blank input is ignored.
func addList(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)
	url := strings.TrimSpace(app.listInput)
	if url == "" {
		return
	}

	app.draft.lists = append(app.draft.lists, listDraft{url: url, enabled: true, idx: -1})
	app.listInput = ""
	app.seeded = true
	ctx.Window.InvalidateLayout()
}

// listToggle flips one list row's enabled flag.
func listToggle(i int) func(gogui.EventCtx) {
	return func(ctx gogui.EventCtx) {
		updateDraft(ctx, func(d *profileDraft) {
			if i >= 0 && i < len(d.lists) {
				d.lists[i].enabled = !d.lists[i].enabled
			}
		})
	}
}

// removeList asks before dropping one list row.  A seeded row removed here
// becomes a Remove-by-index in the request; a new row simply disappears.  The
// row is only removed once the confirm dialog is accepted.
func removeList(i int) func(gogui.EventCtx) {
	return func(ctx gogui.EventCtx) {
		confirmRemove(ctx, "Remove list", "This list will be removed when you save the profile.",
			fmt.Sprintf("gui:list:%d:remove:confirm", i), func(d *profileDraft) {
				if i >= 0 && i < len(d.lists) {
					d.lists = append(d.lists[:i], d.lists[i+1:]...)
				}
			})
	}
}

// addRule appends an empty per-domain rule row for the user to fill in.  idx is
// -1: a brand-new rule has no existing target to inherit, so it must carry an
// upstream.  It starts enabled.
func addRule(ctx gogui.EventCtx) {
	updateDraft(ctx, func(d *profileDraft) {
		d.perDomain = append(d.perDomain, ruleDraft{enabled: true, idx: -1})
	})
}

// setRuleDomains stores one rule's comma-separated domain input.
func setRuleDomains(i int, text string, ctx gogui.EventCtx) {
	updateDraft(ctx, func(d *profileDraft) {
		if i >= 0 && i < len(d.perDomain) {
			d.perDomain[i].domains = text
		}
	})
}

// setRuleUpstream stores one rule's upstream input (blank = keep).
func setRuleUpstream(i int, text string, ctx gogui.EventCtx) {
	updateDraft(ctx, func(d *profileDraft) {
		if i >= 0 && i < len(d.perDomain) {
			d.perDomain[i].upstream = text
		}
	})
}

// setRuleType switches one rule between the upstream and bypass kinds.  A
// bypass rule carries no upstream, so a typed value is simply not sent while
// the rule is bypass (and is still there if the user switches back).
func setRuleType(i int, kind string, ctx gogui.EventCtx) {
	updateDraft(ctx, func(d *profileDraft) {
		if i >= 0 && i < len(d.perDomain) {
			d.perDomain[i].bypass = kind == ruleKindBypass
		}
	})
}

// ruleToggle flips one per-domain rule's enabled flag.
func ruleToggle(i int) func(gogui.EventCtx) {
	return func(ctx gogui.EventCtx) {
		updateDraft(ctx, func(d *profileDraft) {
			if i >= 0 && i < len(d.perDomain) {
				d.perDomain[i].enabled = !d.perDomain[i].enabled
			}
		})
	}
}

// removeRule asks before dropping one per-domain rule row.  The row is only
// removed once the confirm dialog is accepted.
func removeRule(i int) func(gogui.EventCtx) {
	return func(ctx gogui.EventCtx) {
		confirmRemove(ctx, "Remove rule", "This rule will be removed when you save the profile.",
			fmt.Sprintf("gui:rule:%d:remove:confirm", i), func(d *profileDraft) {
				if i >= 0 && i < len(d.perDomain) {
					d.perDomain = append(d.perDomain[:i], d.perDomain[i+1:]...)
				}
			})
	}
}

// confirmRemove asks for confirmation, then applies a bounds-checked row
// removal to the draft.
func confirmRemove(ctx gogui.EventCtx, title, body, focusID string, apply func(*profileDraft)) {
	ctx.Window.Dialog(gogui.DialogCfg{
		DialogType: gogui.DialogConfirm,
		Title:      title,
		Body:       body,
		FocusID:    focusID,
		OnOkYes: func(w *gogui.Window) {
			updateDraft(gogui.EventCtx{Window: w}, apply)
		},
	})
}

// setPage switches the visible screen.
func setPage(ctx gogui.EventCtx, p pageKind) {
	app := gogui.State[App](ctx.Window)
	if app.page != p {
		app.page = p
		requestRefresh(app.refresh) // a newly shown page should show fresh data
		ctx.Window.InvalidateLayout()
	}
	// Opening the Logging page must land on the newest line.  A fresh layout
	// would otherwise paint the tail from the top; the same click while already
	// there re-pins, so the nav item always jumps to the live end.
	if p == pageLogging {
		pinLogToBottom(ctx.Window)
	}
}

// --- profile management dialogs ---

// promptAddProfile asks for a name, then dispatches the add through the
// elevated apply seam.
func promptAddProfile(ctx gogui.EventCtx) {
	ctx.Window.Dialog(gogui.DialogCfg{
		DialogType: gogui.DialogPrompt,
		Title:      "Add profile",
		Body:       "Name the new profile.",
		FocusID:    "gui:profile:add:name",
		OnReply:    func(reply string, w *gogui.Window) { addProfile(w, reply) },
	})
}

// promptRenameProfile asks for a new name for the selected profile.
func promptRenameProfile(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)
	current := selectedProfile(app)

	ctx.Window.Dialog(gogui.DialogCfg{
		DialogType: gogui.DialogPrompt,
		Title:      "Rename profile",
		Body:       "Enter a new name for \"" + current + "\".",
		Reply:      current,
		FocusID:    "gui:profile:rename:name",
		OnReply:    func(reply string, w *gogui.Window) { renameProfile(w, current, reply) },
	})
}

// confirmDeleteProfile asks before removing the selected profile.
func confirmDeleteProfile(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)
	name := selectedProfile(app)

	ctx.Window.Dialog(gogui.DialogCfg{
		DialogType: gogui.DialogConfirm,
		Title:      "Delete profile",
		Body:       "Delete \"" + name + "\"? This cannot be undone.",
		FocusID:    "gui:profile:delete:confirm",
		OnOkYes:    func(w *gogui.Window) { deleteProfile(w, name) },
	})
}

// addProfile dispatches an add for a trimmed, non-empty name.  The helper
// rejects a duplicate; the list refreshes on the next poll.
func addProfile(w *gogui.Window, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}

	runProfileOp(w, profileOp{kind: opAdd, name: name}, nil)
}

// renameProfile dispatches a rename, carrying the editor's pending edits for
// the renamed profile when there are any.
func renameProfile(w *gogui.Window, from, to string) {
	app := gogui.State[App](w)
	to = strings.TrimSpace(to)
	if to == "" || to == from {
		return
	}

	var draft *profileDraft
	if app.draftDirty() {
		d := app.draft
		draft = &d
	}

	runProfileOp(w, profileOp{kind: opRename, name: from, to: to}, draft)
}

// deleteProfile dispatches a delete for a profile the rules allow removing.
func deleteProfile(w *gogui.Window, name string) {
	app := gogui.State[App](w)
	if deleteDisabled(app.profiles, app.active, name) {
		return
	}

	runProfileOp(w, profileOp{kind: opDelete, name: name}, nil)
}

// --- small pure helpers ---

// profilesFor is the known profile list, falling back to the active name.
func profilesFor(app *App) []string {
	if len(app.profiles) > 0 {
		return app.profiles
	}
	if app.active != "" {
		return []string{app.active}
	}

	return nil
}

// selectedProfile is the profile the editor is focused on.
func selectedProfile(app *App) (name string) {
	if app.selected != "" {
		return app.selected
	}

	return app.active
}

// providerOptions lists the provider catalog as label/value options.  The
// catalog is local and secret-free (SPEC §15.1).  The label is only the human
// name: the backend id and the parenthetical detail live on the value and in
// the caption under the select, so the menu does not read "controld — ControlD".
func providerOptions() (opts []gogui.SelectOption) {
	for _, p := range providers.Registry() {
		opts = append(opts, gogui.NewSelectOption(providerLabel(p.Name), p.Name))
	}

	return opts
}

// providerLabel is a provider's human display name.
func providerLabel(name string) (label string) {
	switch name {
	case providers.Quad9:
		return "Quad9"
	case providers.Cloudflare:
		return "Cloudflare"
	case providers.NextDNS:
		return "NextDNS"
	case providers.ControlD:
		return "ControlD"
	case providers.Custom:
		return "Custom"
	default:
		return name
	}
}

// providerDetail is the small line under the provider select: the one action
// the chosen provider needs, in plain language.  A provider that needs nothing
// gets no line, so the form stays quiet.  The provider name is already in the
// select, so it is never repeated here.
func providerDetail(provider, variant string) (detail string) {
	if needsResolverID(provider, variant) {
		return "Requires a Resolver ID to be set."
	}

	return ""
}

// resolverIDHint is the line under the Resolver ID field.  It names the value
// to enter for the current provider and variant; the provider and variant
// options stay free of that instruction.  Unknown selections fall back to a
// neutral keep note, never to CLI syntax.
func resolverIDHint(provider, variant string) (hint string) {
	if !needsResolverID(provider, variant) {
		return "This provider does not use a Resolver ID."
	}

	switch {
	case provider == providers.NextDNS:
		return "Enter your NextDNS configuration ID."
	case provider == providers.ControlD && variant == "custom":
		return "Enter the profile ID from your ControlD dashboard."
	case provider == providers.ControlD && variant == "x-hagezi":
		return "Enter the Hagezi list name, for example x-hagezi-pro or x-hagezi-pro-plus."
	case provider == providers.ControlD && variant == "free":
		return "Enter the ControlD free preset name."
	}

	return "Leave blank to keep the current Resolver ID."
}

// variantLabels is the GUI's display copy for the catalog's variants.  It is
// deliberately separate from providers.Variant.Description, which the CLI
// prints verbatim (backend names and --id examples included).  Keys are
// "provider/variant".
var variantLabels = map[string]string{
	"quad9/secure":           "Malware blocking, recommended",
	"quad9/noblock":          "No threat blocking",
	"quad9/secure-ecs":       "Malware blocking, shares a network prefix",
	"quad9/noblock-ecs":      "No blocking, shares a network prefix",
	"cloudflare/standard":    "Unfiltered",
	"cloudflare/security":    "Malware blocking",
	"cloudflare/family":      "Malware and adult-content blocking",
	"nextdns/ultralow":       "Fastest routing, uses system DNS once",
	"nextdns/anycast":        "Fixed anycast addresses, slower routing",
	"controld/p0":            "Unfiltered",
	"controld/p1":            "Malware blocked",
	"controld/p2":            "Ads and trackers blocked",
	"controld/p3":            "Social media blocked",
	"controld/x-oisd":        "OISD blocklist",
	"controld/x-stevenblack": "StevenBlack blocklist",
	"controld/x-adguard":     "AdGuard blocklist",
	"controld/family":        "Family-friendly",
	"controld/uncensored":    "Uncensored",
	"controld/custom":        "Custom profile",
	"controld/x-hagezi":      "Hagezi lists",
	"controld/free":          "Free preset",
	"custom/custom":          "Custom URL",
}

// variantLabel is a variant's human display name, with the raw variant name as
// a fallback for a catalog addition this table has not covered yet.
func variantLabel(provider string, v providers.Variant) (label string) {
	return variantNameLabel(provider, v.Name)
}

// variantNameLabel is variantLabel for a stored/summary variant name, so the
// Status page's chips and Connection Info rows reuse the same display copy.
func variantNameLabel(provider, variant string) (label string) {
	if label, ok := variantLabels[provider+"/"+variant]; ok {
		return label
	}

	return variant
}

// variantOptions lists the selected provider's variants.
func variantOptions(provider string) (opts []gogui.SelectOption) {
	p, ok := providers.Lookup(provider)
	if !ok {
		return nil
	}

	for _, v := range p.Variants {
		opts = append(opts, gogui.NewSelectOption(variantLabel(provider, v), v.Name))
	}

	return opts
}

// protocolOptions lists the supported transports.
func protocolOptions() (opts []gogui.SelectOption) {
	for _, p := range providers.Protocols() {
		opts = append(opts, gogui.NewSelectOption(protocolLabel(p), string(p)))
	}

	return opts
}

// protocolSelect is the transport control.  It is rendered only for providers
// whose transport is a real choice; the custom provider takes its transport
// from the URL and shows a hint instead.
func protocolSelect(d profileDraft, disabled bool, floor float32) gogui.View {
	return formSelect(gogui.SelectCfg{
		ID: "gui:edit:protocol", Label: "Protocol",
		Options: protocolOptions(), Selected: one(d.protocol),
		Disabled: disabled,
		OnSelect: func(vals []string, ctx gogui.EventCtx) {
			updateDraft(ctx, func(d *profileDraft) {
				d.protocol = first(vals)
				syncBootstrap(d)
			})
		},
	}, floor)
}

// blockResponseOptions lists the block-response choices.
func blockResponseOptions() (opts []gogui.SelectOption) {
	return []gogui.SelectOption{
		gogui.NewSelectOption("0.0.0.0", "0.0.0.0"),
		gogui.NewSelectOption("NXDOMAIN", "nxdomain"),
	}
}

// defaultVariant is a provider's default variant name.
func defaultVariant(name string) (variant string) {
	if p, ok := providers.Lookup(name); ok {
		return p.DefaultVariant
	}

	return ""
}

// protocolLabel renders a transport for display.
func protocolLabel(p providers.Protocol) (label string) {
	switch p {
	case providers.DoH:
		return "DoH (HTTPS)"
	case providers.DoH3:
		return "DoH3 (HTTP/3)"
	case providers.DoT:
		return "DoT (TLS)"
	case providers.DoQ:
		return "DoQ (QUIC)"
	default:
		return string(p)
	}
}

// one wraps a value as a single-item Select selection, or nil when empty.
func one(v string) []string {
	if v == "" {
		return nil
	}

	return []string{v}
}

// first returns the first element or "".
func first(vals []string) (v string) {
	if len(vals) == 0 {
		return ""
	}

	return vals[0]
}
