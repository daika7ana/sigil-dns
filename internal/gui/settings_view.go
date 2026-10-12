//go:build gui

package gui

import (
	"fmt"
	"strings"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/daika7ana/dns-client/internal/providers"
	"github.com/daika7ana/dns-client/internal/version"
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

	content := []gogui.View{}
	if !canEdit(app.snap) {
		content = append(content, notice(t,
			"Service identity not verified",
			"Editing is unavailable until the dns-client service is verified."))
	} else if hint := editingHint(app); hint != "" {
		content = append(content, notice(t, "Offline editing", hint))
	}

	content = append(content,
		serviceCard(t, app),
		profilesCard(t, app, floor),
		editorCard(t, app, floor),
		versionCaption(t, app),
	)

	return gogui.Column(gogui.ContainerCfg{
		ID:         "gui:settings",
		Sizing:     gogui.FillFill,
		Spacing:    gogui.SpacingMedium,
		Scrollable: true,
		Content:    content,
	})
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

// refreshProfiles updates the profile list, the active name, and the
// summaries from the effective summary.  If the selected profile vanished
// (renamed or deleted elsewhere), the editor falls back to the active profile.
func (a *App) refreshProfiles() {
	sum := a.summary()
	if sum == nil {
		return
	}

	a.summaries = sum.Profiles

	names := profileNames(a.summaries)
	if len(names) > 0 {
		a.profiles = names
	}
	if active := activeProfileName(sum); active != "" {
		a.active = active
	}

	if a.selected != "" && len(names) > 0 && !contains(names, a.selected) {
		a.selected = a.active
		a.draft = draftFromSummary(sum, a.selected)
		a.seeded = true
	}
}

// serviceCard is the one-time service lifecycle: Install when absent,
// Uninstall when present.  Routine Start/Stop lives behind the Status page's
// single Protection control (Enable starts the service), so it is not exposed
// here.  Writes are gated by the unverified-trust rule and the busy flag.
func serviceCard(t gogui.Theme, app *App) gogui.View {
	disabled := app.busy || !canEdit(app.snap)

	action := actionInstall
	if app.snap.installed {
		action = actionUninstall
	}

	return card(t, "gui:card:service", "Service", []gogui.View{
		kv(t, "Installed", yesNo(app.snap.installed)),
		kv(t, "State", fallback(app.snap.scmState, "unknown")),
		gogui.Button(gogui.ButtonCfg{
			ID:       "gui:" + string(action),
			Variant:  actionVariant(action),
			Label:    actionLabel(action) + " service",
			Disabled: disabled,
			OnClick:  func(ctx gogui.EventCtx) { runAction(ctx, action) },
		}),
	})
}

// versionCaption is the small, low-value build line moved off the Status page.
func versionCaption(t gogui.Theme, app *App) gogui.View {
	service := "—"
	proto := "—"
	if app.snap.info != nil {
		service = fallback(app.snap.info.AppVersion, version.Version)
		proto = fmt.Sprintf("%d", app.snap.info.ProtocolVersion)
	}

	return gogui.Text(gogui.TextCfg{
		Text:      fmt.Sprintf("Client %s · Service %s · Protocol %s", version.Version, service, proto),
		TextStyle: t.TextStyleCaption,
		Sizing:    gogui.FillFit,
	})
}

// selectFloor is the shared width floor for every Settings dropdown.  The
// pinned toolkit sizes an open popup from MinWidth/MaxWidth plus its own
// content and has no "fill to the field" lever; MinWidth is also applied to the
// closed field, but only as a floor, so a value below the field's own width
// leaves the filling field untouched while lifting every popup close to it.
//
// The field width is the window minus fixed chrome (nav rail plus paddings),
// so the floor tracks the window and the ratio/cap keep it just under the field
// to avoid ever growing the field past its card.  Long-content lists (Provider,
// Variant) may still exceed the floor — the toolkit gives no way to cap the
// popup without also capping the field.
func selectFloor(windowWidth int) float32 {
	const (
		chrome   float32 = 238 // nav rail + root/card padding, measured
		ratio    float32 = 0.9
		minFloor float32 = 200
	)

	floor := (float32(windowWidth) - chrome) * ratio
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
		gogui.Row(gogui.ContainerCfg{
			Sizing:     gogui.FillFit,
			Spacing:    gogui.SpacingSmall,
			Padding:    gogui.PaddingNone,
			SizeBorder: gogui.NoBorder,
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
					ID: "gui:profile:delete", Variant: gogui.ButtonDanger, Label: "Delete",
					Sizing:   gogui.FillFit,
					Disabled: disabled || deleteDisabled(names, app.active, selected),
					OnClick:  confirmDeleteProfile,
				}),
				gogui.Button(gogui.ButtonCfg{
					ID: "gui:profile:active", Variant: gogui.ButtonSecondary, Label: "Set active",
					Sizing:   gogui.FillFit,
					Disabled: disabled || selected == "" || selected == app.active,
					OnClick: func(ctx gogui.EventCtx) {
						runSetActive(ctx.Window, selectedProfile(app))
					},
				}),
			},
		}),
	})
}

// editorCard is the per-profile editor for the selected profile.  It is
// editable whenever there is a profile to edit — including the built-in default
// seeded on a fresh install — and the identity check passes.
func editorCard(t gogui.Theme, app *App, floor float32) gogui.View {
	d := app.draft
	disabled := app.busy || !canEdit(app.snap) || d.name == ""

	fields := []gogui.View{
		formSelect(gogui.SelectCfg{
			ID: "gui:edit:provider", Label: "Provider",
			Options: providerOptions(), Selected: one(d.provider),
			Disabled: disabled, OnSelect: setDraftProvider,
		}, floor),
		caption(t, providerDescription(d.provider)),
		formSelect(gogui.SelectCfg{
			ID: "gui:edit:variant", Label: "Variant",
			Options: variantOptions(d.provider), Selected: one(d.variant),
			Disabled: disabled,
			OnSelect: func(vals []string, ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) {
					d.variant = first(vals)
					syncBootstrap(d)
				})
			},
		}, floor),
	}

	// The custom provider's transport is dictated by its URL, so there is no
	// protocol choice to make; show a one-line hint instead of the control.
	if d.provider == providers.Custom {
		fields = append(fields, caption(t, "Transport is taken from the custom URL."))
	} else {
		fields = append(fields, protocolSelect(d, disabled, floor))
	}

	// The resolver id is only meaningful for providers that use one; the
	// field stays visible but inert otherwise, with a line explaining why.
	// The state is derived per frame from the current provider+variant, so
	// switching provider updates it on the next repaint.
	idNeeded := needsResolverID(d.provider, d.variant)

	fields = append(fields, gogui.Input(gogui.InputCfg{
		ID: "gui:edit:id", Label: "Resolver ID", Sizing: gogui.FillFit,
		Text: d.id, IsPassword: true, Placeholder: "•••• leave blank to keep",
		Disabled: disabled || !idNeeded,
		OnTextChanged: func(text string, ctx gogui.EventCtx) {
			updateDraft(ctx, func(d *profileDraft) {
				d.id = text
				syncBootstrap(d)
			})
		},
	}))
	if !idNeeded {
		fields = append(fields, gogui.Text(gogui.TextCfg{
			ID:        "gui:edit:id:hint",
			Text:      "This provider does not use a resolver ID.",
			TextStyle: t.TextStyleCaption,
			Sizing:    gogui.FillFit,
			Mode:      gogui.TextModeWrap,
		}))
	}

	if d.provider == providers.Custom {
		fields = append(fields, gogui.Input(gogui.InputCfg{
			ID: "gui:edit:url", Label: "Custom URL", Sizing: gogui.FillFit,
			Text: d.url, Placeholder: "leave blank to keep", Disabled: disabled,
			OnTextChanged: func(text string, ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) { d.url = text })
			},
		}))
	}

	fields = append(fields,
		gogui.Input(gogui.InputCfg{
			ID: "gui:edit:bootstrap", Label: "Bootstrap (comma-separated)",
			Sizing: gogui.FillFit,
			Text:   d.bootstrap, Disabled: disabled,
			OnTextChanged: func(text string, ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) { d.bootstrap = text })
			},
		}),
		gogui.Toggle(gogui.ToggleCfg{
			ID: "gui:edit:filtering", Label: "Filtering", Selected: d.filtering,
			Disabled: disabled,
			OnClick: func(ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) { d.filtering = !d.filtering })
			},
		}),
		formSelect(gogui.SelectCfg{
			ID: "gui:edit:block", Label: "Block response",
			Options: blockResponseOptions(), Selected: one(d.blockResp),
			Disabled: disabled || !d.filtering,
			OnSelect: func(vals []string, ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) { d.blockResp = first(vals) })
			},
		}, floor),
		gogui.Toggle(gogui.ToggleCfg{
			ID: "gui:edit:cache", Label: "Cache", Selected: d.cache,
			Disabled: disabled,
			OnClick: func(ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) { d.cache = !d.cache })
			},
		}),
		gogui.Input(gogui.InputCfg{
			ID: "gui:edit:cachesize", Label: "Cache size (MiB)",
			Sizing: gogui.FillFit,
			Text:   d.cacheSizeMB, Disabled: disabled || !d.cache,
			OnTextChanged: func(text string, ctx gogui.EventCtx) {
				updateDraft(ctx, func(d *profileDraft) { d.cacheSizeMB = text })
			},
		}),
		saveRow(t, app, d, disabled),
	)

	return card(t, "gui:card:editor", "Profile editor", fields)
}

// saveRow is the Save button and its one-line hint.  Save spans the full field
// column (PaddingNone, no blank left inset) so it lines up with the inputs above
// it; the hint sits underneath.
func saveRow(t gogui.Theme, app *App, d profileDraft, disabled bool) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		Spacing:    gogui.SpacingTight,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			gogui.Button(gogui.ButtonCfg{
				ID: "gui:profile:save", Variant: gogui.ButtonPrimary, Label: "Save",
				Sizing: gogui.FillFit, Disabled: disabled,
				OnClick: func(ctx gogui.EventCtx) { runSaveProfile(ctx, d) },
			}),
			gogui.Text(gogui.TextCfg{
				Text:      "Blank resolver ID keeps the current one.",
				TextStyle: t.TextStyleCaption,
				Sizing:    gogui.FillFit,
				Mode:      gogui.TextModeWrap,
			}),
		},
	})
}

// notice is a calm inline banner for a blocking condition.
func notice(t gogui.Theme, title, detail string) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		Color:   t.ColorWarningSubtle,
		Radius:  gogui.RadiusMedium,
		Padding: t.PaddingMedium,
		Spacing: gogui.SpacingTight,
		Content: []gogui.View{
			gogui.Text(gogui.TextCfg{Text: title, TextStyle: t.TextStyleWarning}),
			gogui.Text(gogui.TextCfg{
				Text:      detail,
				TextStyle: t.TextStyleSecondary,
				Mode:      gogui.TextModeWrap,
			}),
		},
	})
}

// caption is a small, wrapped secondary line under a field.
func caption(t gogui.Theme, s string) gogui.View {
	return gogui.Text(gogui.TextCfg{
		Text:      s,
		TextStyle: t.TextStyleCaption,
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
	app.seeded = true
	ctx.Window.InvalidateLayout()
}

// setPage switches the visible screen.
func setPage(ctx gogui.EventCtx, p pageKind) {
	app := gogui.State[App](ctx.Window)
	if app.page == p {
		return
	}
	app.page = p
	ctx.Window.InvalidateLayout()
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
		Body:       "Rename \"" + current + "\".",
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
// catalog is local and secret-free (SPEC §15.1).
func providerOptions() (opts []gogui.SelectOption) {
	for _, p := range providers.Registry() {
		opts = append(opts, gogui.NewSelectOption(p.Name+" — "+p.Description, p.Name))
	}

	return opts
}

// variantOptions lists the selected provider's variants.
func variantOptions(provider string) (opts []gogui.SelectOption) {
	p, ok := providers.Lookup(provider)
	if !ok {
		return nil
	}

	for _, v := range p.Variants {
		label := v.Name
		if v.Description != "" {
			label += " — " + v.Description
		}
		opts = append(opts, gogui.NewSelectOption(label, v.Name))
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
		gogui.NewSelectOption("nxdomain", "nxdomain"),
	}
}

// providerDescription is the selected provider's one-line description.
func providerDescription(name string) (desc string) {
	if p, ok := providers.Lookup(name); ok {
		return p.Description
	}

	return ""
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

// contains reports whether names holds name.
func contains(names []string, name string) (ok bool) {
	for _, n := range names {
		if n == name {
			return true
		}
	}

	return false
}
