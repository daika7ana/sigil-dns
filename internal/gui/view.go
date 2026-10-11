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
	v := statusVisualFor(app.snap, t)

	return gogui.Column(gogui.ContainerCfg{
		Sizing:  gogui.FillFill,
		Color:   t.ColorBackground,
		Padding: t.PaddingLarge,
		Spacing: gogui.SpacingLarge,
		Content: []gogui.View{
			header(t, app),
			banner(t, v),
			gogui.Column(gogui.ContainerCfg{
				ID:         "gui:content",
				Sizing:     gogui.FillFill,
				Spacing:    gogui.SpacingLarge,
				Scrollable: true,
				Content:    sections(app.snap, v, t),
			}),
			footer(t, app),
		},
	})
}

// header is the app title, a subtitle, and the window controls.
func header(t gogui.Theme, app *App) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		Sizing:  gogui.FillFit,
		Spacing: gogui.SpacingMedium,
		VAlign:  gogui.VAlignMiddle,
		Content: []gogui.View{
			gogui.Column(gogui.ContainerCfg{
				Sizing:  gogui.FillFit,
				Spacing: gogui.SpacingTight,
				Content: []gogui.View{
					gogui.Text(gogui.TextCfg{
						Text:      "dns-client",
						TextStyle: t.TextStyleTitle,
					}),
					gogui.Text(gogui.TextCfg{
						Text:      "Loopback encrypted DNS status",
						TextStyle: t.TextStyleSecondary,
					}),
				},
			}),
			gogui.Row(gogui.ContainerCfg{
				Sizing:  gogui.FixedFit,
				Spacing: gogui.SpacingSmall,
				VAlign:  gogui.VAlignMiddle,
				Content: []gogui.View{
					gogui.Button(gogui.ButtonCfg{
						ID:      "gui:theme",
						Variant: gogui.ButtonGhost,
						Label:   themeLabel(app.themePref),
						OnClick: cycleTheme,
					}),
					gogui.Button(gogui.ButtonCfg{
						ID:      "gui:refresh",
						Variant: gogui.ButtonSecondary,
						Label:   "Refresh",
						OnClick: requestRefresh,
					}),
				},
			}),
		},
	})
}

// banner is the state headline: a colored dot, a headline, and one sentence.
func banner(t gogui.Theme, v statusVisual) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		Sizing:  gogui.FillFit,
		Color:   v.bg,
		Radius:  gogui.RadiusMedium,
		Padding: t.PaddingMedium,
		Spacing: gogui.SpacingMedium,
		VAlign:  gogui.VAlignTop,
		Content: []gogui.View{
			gogui.Circle(gogui.ContainerCfg{
				Width:      10,
				Height:     10,
				Sizing:     gogui.FixedFixed,
				Color:      v.accent,
				Padding:    gogui.PaddingNone,
				SizeBorder: gogui.NoBorder,
			}),
			gogui.Column(gogui.ContainerCfg{
				Sizing:  gogui.FillFit,
				Spacing: gogui.SpacingTight,
				Content: []gogui.View{
					gogui.Text(gogui.TextCfg{Text: v.headline, TextStyle: v.headlineStyle}),
					gogui.Text(gogui.TextCfg{
						Text:      v.detail,
						TextStyle: t.TextStyleSecondary,
						Mode:      gogui.TextModeWrap,
					}),
				},
			}),
		},
	})
}

// sections returns the body groups for the current state.  Stopped and
// unverified states show no service data, only the client's own build info; a
// degraded (unloadable) config drops the config-dependent groups, leaving the
// runtime state and the build info.
func sections(s snapshot, v statusVisual, t gogui.Theme) []gogui.View {
	switch {
	case v.kind == kindStopped || v.kind == kindUnverified:
		return []gogui.View{aboutSection(s, t)}
	case v.kind == kindDegraded && s.summary != nil && !s.summary.ConfigValid:
		return []gogui.View{serviceSection(s, t), aboutSection(s, t)}
	default:
		return []gogui.View{
			profileSection(s, t),
			filteringSection(s, t),
			cacheSection(s, t),
			serviceSection(s, t),
			aboutSection(s, t),
		}
	}
}

// profileSection renders the active profile's display-only labels and a masked
// resolver id (SPEC §15.2: never the id itself).
func profileSection(s snapshot, t gogui.Theme) gogui.View {
	sum := s.summary
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

	return card(t, "Profile", rows)
}

// filteringSection renders filtering settings, list hosts, and rule counts.
func filteringSection(s snapshot, t gogui.Theme) gogui.View {
	sum := s.summary
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

	return card(t, "Filtering", rows)
}

// cacheSection renders cache and bootstrap settings.
func cacheSection(s snapshot, t gogui.Theme) gogui.View {
	sum := s.summary
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

	return card(t, "Cache & bootstrap", rows)
}

// serviceSection renders the runtime state from the status DTO.
func serviceSection(s snapshot, t gogui.Theme) gogui.View {
	st := s.status
	rows := []gogui.View{}

	if st == nil {
		rows = append(rows, kv(t, "State", "—"))
	} else {
		rows = append(rows,
			kv(t, "Enabled", yesNo(st.Enabled)),
			kv(t, "Mode", fallback(st.Mode, "—")),
			kv(t, "Mechanism", fallback(st.Mechanism, "—")),
			kv(t, "Dirty marker", yesNo(st.Dirty)),
			kv(t, "Last health", healthValue(st.Health)),
		)
	}

	return card(t, "Service", rows)
}

// aboutSection renders the client and service versions and capabilities.
func aboutSection(s snapshot, t gogui.Theme) gogui.View {
	serviceVersion := "—"
	proto := "—"
	caps := "—"

	if s.info != nil {
		serviceVersion = fallback(s.info.AppVersion, version.Version)
		proto = fmt.Sprintf("%d", s.info.ProtocolVersion)
		caps = capabilities(s.info.Capabilities)
	}

	return card(t, "About", []gogui.View{
		kv(t, "Client version", version.Version),
		kv(t, "Service version", serviceVersion),
		kv(t, "Protocol", proto),
		kv(t, "Capabilities", caps),
	})
}

// footer carries the last-updated timestamp.
func footer(t gogui.Theme, app *App) gogui.View {
	updated := "Updated never"
	if !app.snap.updated.IsZero() {
		updated = "Updated " + app.snap.updated.Format("15:04:05")
	}

	return gogui.Row(gogui.ContainerCfg{
		Sizing: gogui.FillFit,
		Content: []gogui.View{
			gogui.Text(gogui.TextCfg{
				Text:      updated,
				TextStyle: t.TextStyleCaption,
				Sizing:    gogui.FillFit,
			}),
		},
	})
}

// card is a titled group-box holding label/value rows.
func card(t gogui.Theme, title string, rows []gogui.View) gogui.View {
	return gogui.Column(gogui.ContainerCfg{
		Title:   title,
		TitleBG: t.ColorBackground,
		Sizing:  gogui.FillFit,
		Spacing: gogui.SpacingSmall,
		Content: rows,
	})
}

// kv is one label/value row with a fixed label column.
func kv(t gogui.Theme, label, value string) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		Sizing:  gogui.FillFit,
		Spacing: gogui.SpacingMedium,
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

// statusVisualFor maps a snapshot onto the state the viewer sees.  Order
// matters: trust and reachability gate everything below them.
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
		return statusVisual{
			kind:          kindStopped,
			headline:      "Not running",
			detail:        "The dns-client service is not installed or not running.",
			headlineStyle: t.TextStyleTitleSmall,
			bg:            t.ColorPanel,
			accent:        t.TextStyleSecondary.Color,
		}
	case s.status != nil && s.status.Dirty:
		return statusVisual{
			kind:          kindDirty,
			headline:      "Restore needed",
			detail:        "Interception is still applied from a previous session. Restart the service to clean up.",
			headlineStyle: t.TextStyleWarning,
			bg:            t.ColorWarningSubtle,
			accent:        t.TextStyleWarning.Color,
		}
	case s.summary != nil && !s.summary.ConfigValid:
		return statusVisual{
			kind:          kindDegraded,
			headline:      "Needs attention",
			detail:        configErrorDetail(s.summary),
			headlineStyle: t.TextStyleWarning,
			bg:            t.ColorWarningSubtle,
			accent:        t.TextStyleWarning.Color,
		}
	case s.status != nil && s.status.Enabled && !healthOK(s.status.Health):
		return statusVisual{
			kind:          kindDegraded,
			headline:      "Needs attention",
			detail:        "The resolver is enabled but its last health check did not pass.",
			headlineStyle: t.TextStyleWarning,
			bg:            t.ColorWarningSubtle,
			accent:        t.TextStyleWarning.Color,
		}
	default:
		return statusVisual{
			kind:          kindRunning,
			headline:      "Protected",
			detail:        "DNS queries are handled by the dns-client loopback resolver.",
			headlineStyle: t.TextStyleSuccess,
			bg:            t.ColorSuccessSubtle,
			accent:        t.TextStyleSuccess.Color,
		}
	}
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

// healthValue renders the health bits as one line.
func healthValue(h *ipc.HealthSummary) (s string) {
	if h == nil {
		return "unknown"
	}

	var parts []string
	if h.Ours {
		parts = append(parts, "loopback")
	}
	if h.IPv4 {
		parts = append(parts, "IPv4")
	}
	if h.IPv6 {
		parts = append(parts, "IPv6")
	}
	if len(parts) == 0 {
		return "failing"
	}

	return "ok (" + strings.Join(parts, ", ") + ")"
}

// capabilities names the info capability bits.
func capabilities(bits uint64) (s string) {
	var names []string
	if bits&ipc.CapInfo != 0 {
		names = append(names, "info")
	}
	if bits&ipc.CapConfigSummary != 0 {
		names = append(names, "config")
	}
	if bits&ipc.CapReload != 0 {
		names = append(names, "reload")
	}
	if len(names) == 0 {
		return "none"
	}

	return strings.Join(names, ", ")
}

// resolverID masks the resolver id: set or unset, never the value (SPEC §15.2).
func resolverID(set bool) (s string) {
	if set {
		return "•••• (set)"
	}

	return "not set"
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
		return "yes"
	}

	return "no"
}

func enabledWord(b bool) (s string) {
	if b {
		return "enabled"
	}

	return "disabled"
}
