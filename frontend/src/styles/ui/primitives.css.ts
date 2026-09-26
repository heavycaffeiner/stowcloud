import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-settings-page", {
    flex: "1",
    width: "min(100%, 60rem)",
    minWidth: "0",
    marginInline: "auto",
    padding: "var(--sc-page-pad)",
    wordBreak: "normal"
});
globalStyle(".sc-settings-page > header", {
    paddingBlockEnd: "4px"
});
globalStyle(".sc-settings-page > header h1", {
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-headline-small-size)",
    fontWeight: "var(--mdui-typescale-headline-small-weight)",
    letterSpacing: "var(--mdui-typescale-headline-small-tracking)",
    lineHeight: "var(--mdui-typescale-headline-small-line-height)"
});
globalStyle(".sc-settings-page-tabs", {
    display: "flex",
    gap: "4px",
    margin: "16px 0 24px",
    padding: "4px",
    overflowX: "auto"
});
globalStyle(".sc-settings-page-tab", {
    display: "flex",
    flex: "none",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    gap: "4px",
    minInlineSize: "48px",
    minBlockSize: "64px",
    padding: "10px 12px",
    border: "none",
    borderRadius: "var(--sc-radius-small)",
    background: "transparent",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-label-large-size)",
    fontWeight: "var(--mdui-typescale-label-large-weight)",
    lineHeight: "var(--mdui-typescale-label-large-line-height)",
    letterSpacing: "var(--mdui-typescale-label-large-tracking)",
    whiteSpace: "nowrap",
    cursor: "pointer",
    transition: "background-color 150ms ease"
});
globalStyle(".sc-settings-page-tab:hover", {
    background: "rgb(var(--mdui-color-surface-container-high))"
});
globalStyle(".sc-settings-page-tab[aria-current=\"page\"]", {
    background: "rgb(var(--mdui-color-secondary-container))",
    color: "rgb(var(--mdui-color-on-secondary-container))"
});
globalStyle(".sc-settings-page-tab:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-settings-card,\n.sc-admin-card", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    padding: "24px 0",
    minWidth: "0",
    border: "0",
    borderBottom: "1px solid var(--sc-outline-variant)",
    borderRadius: "0",
    background: "transparent",
    color: "var(--sc-content-primary)",
    boxShadow: "none"
});
globalStyle(".sc-settings-card:last-child,\n.sc-admin-card:last-child", {
    borderBottom: "0"
});
globalStyle(".sc-settings-card-head,\n.sc-admin-card-head", {
    display: "flex",
    alignItems: "flex-start",
    gap: "16px",
    minWidth: "0",
    marginBottom: "4px"
});
globalStyle(".sc-settings-card h2,\n.sc-admin-card-title", {
    margin: "0 0 2px",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-title-large-size)",
    fontWeight: "var(--mdui-typescale-title-large-weight)",
    letterSpacing: "var(--mdui-typescale-title-large-tracking)",
    lineHeight: "var(--mdui-typescale-title-large-line-height)"
});
globalStyle(".sc-settings-page-tabs", {
    "@media": {
        "(max-width: 599.98px)": {
            marginBlockEnd: "16px"
        }
    }
});
globalStyle(".sc-settings-page-tab", {
    "@media": {
        "(max-width: 599.98px)": {
            paddingInline: "8px"
        }
    }
});
globalStyle(".sc-settings-card, .sc-admin-card", {
    "@media": {
        "(max-width: 599.98px)": {
            padding: "16px 0",
            gap: "12px"
        }
    }
});
globalStyle(".sc-settings-card-head, .sc-admin-card-head", {
    "@media": {
        "(max-width: 599.98px)": {
            gap: "12px"
        }
    }
});
globalStyle(".sc-checkbox,\n.sc-switch-row", {
    display: "inline-flex",
    alignItems: "center",
    gap: "12px",
    minHeight: "48px",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-body-large-size)",
    fontWeight: "var(--mdui-typescale-body-large-weight)",
    lineHeight: "var(--mdui-typescale-body-large-line-height)",
    cursor: "pointer",
    flexShrink: "0"
});
globalStyle(".sc-checkbox > mdui-checkbox,\n.sc-switch-row > mdui-switch", {
    flex: "none"
});
globalStyle(".sc-checkbox > span,\n.sc-switch-row > span", {
    minWidth: "0"
});
globalStyle(".sc-switch-row--disabled", {
    cursor: "not-allowed",
    opacity: "0.38"
});
globalStyle(".sc-button-wrap", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    verticalAlign: "middle"
});
globalStyle(".sc-button-wrap > mdui-button", {
    display: "inline-flex"
});
globalStyle("mdui-button.sc-button--square", {
    inlineSize: "var(--sc-control-min)",
    minInlineSize: "var(--sc-control-min)",
    minBlockSize: "var(--sc-control-min)",
    paddingInline: "0"
});
globalStyle("mdui-button.sc-button--square::part(button)", {
    paddingInline: "0",
    justifyContent: "center"
});
globalStyle("mdui-button.sc-button--square::part(label)", {
    display: "none"
});
globalStyle("mdui-button[variant=\"outlined\"]", {
    border: "none !important",
    backgroundColor: "rgb(var(--mdui-color-surface-container-high)) !important",
    color: "rgb(var(--mdui-color-on-surface)) !important",
    transition: "background-color 140ms ease, transform 120ms cubic-bezier(0.2, 0, 0, 1), box-shadow 150ms ease !important"
});
globalStyle("mdui-button[variant=\"outlined\"]:hover", {
    backgroundColor: "rgb(var(--mdui-color-surface-container-highest)) !important",
    color: "rgb(var(--mdui-color-primary)) !important"
});
globalStyle("mdui-button[variant=\"outlined\"][disabled]", {
    backgroundColor: "rgba(var(--mdui-color-on-surface), 0.12) !important",
    color: "rgba(var(--mdui-color-on-surface), 0.38) !important"
});
globalStyle("mdui-segmented-button-group", {
    backgroundColor: "rgb(var(--mdui-color-surface-container-high))",
    borderRadius: "var(--mdui-shape-corner-full)",
    padding: "4px",
    boxSizing: "border-box",
    display: "inline-flex",
    gap: "2px",
    vars: { "--mdui-color-outline": "transparent" }
});
globalStyle("mdui-segmented-button", {
    border: "none !important",
    borderRadius: "var(--mdui-shape-corner-full) !important",
    transition: "background-color 140ms ease, color 140ms ease, box-shadow 140ms ease",
    vars: { "--mdui-color-outline": "transparent" }
});
globalStyle("mdui-segmented-button[selected]", {
    backgroundColor: "rgb(var(--mdui-color-secondary-container)) !important",
    color: "rgb(var(--mdui-color-on-secondary-container)) !important",
    boxShadow: "var(--sc-elevation-1)"
});
globalStyle("mdui-segmented-button:not([selected]):hover", {
    backgroundColor: "rgb(var(--mdui-color-surface-container-highest)) !important"
});
globalStyle("mdui-button::part(button),\nmdui-segmented-button::part(button)", {
    alignItems: "center",
    justifyContent: "center"
});
globalStyle("mdui-menu-item::part(container)", {
    alignItems: "center"
});
globalStyle("mdui-button::part(label),\nmdui-segmented-button::part(label),\nmdui-menu-item::part(label)", {
    display: "flex",
    alignItems: "center",
    alignSelf: "stretch",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)"
});
globalStyle("mdui-button [slot=\"icon\"],\nmdui-button [slot=\"end-icon\"],\nmdui-button-icon [slot=\"icon\"],\nmdui-button-icon > svg", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    verticalAlign: "middle"
});
globalStyle(".sc-button-loading-label", {
    visibility: "hidden"
});
globalStyle(".sc-icon-button", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    verticalAlign: "middle",
    position: "relative",
    flex: "none",
    minInlineSize: "var(--sc-control-min)",
    minBlockSize: "var(--sc-control-min)"
});
globalStyle(".sc-icon-button mdui-button-icon", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    inlineSize: "var(--sc-control-min)",
    blockSize: "var(--sc-control-min)"
});
globalStyle("mdui-button::part(button):focus-visible,\nmdui-button-icon::part(button):focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-icon-button-tip", {
    position: "fixed",
    zIndex: "100",
    inset: "auto",
    margin: "0",
    maxInlineSize: "240px",
    padding: "6px 10px",
    overflow: "hidden",
    border: "0",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-inverse-surface))",
    color: "rgb(var(--mdui-color-inverse-on-surface))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    whiteSpace: "nowrap",
    textOverflow: "ellipsis",
    pointerEvents: "none",
    transform: "translateX(-50%)",
    opacity: "0"
});
globalStyle(".sc-icon-button-tip--placed", {
    opacity: "1"
});
globalStyle(".sc-icon-button-tip", {
    "@media": {
        "(hover: none), (pointer: coarse)": {
            display: "none"
        }
    }
});
globalStyle(".sc-field,\n.sc-select", {
    display: "flex",
    flexDirection: "column",
    minInlineSize: "0",
    inlineSize: "100%",
    maxInlineSize: "35rem"
});
globalStyle(".sc-field > mdui-text-field", {
    display: "block",
    inlineSize: "100%",
    minInlineSize: "0"
});
globalStyle("mdui-dialog mdui-text-field", {
    vars: { "--mdui-color-surface": "var(--mdui-color-surface-container-high)" }
});
globalStyle("mdui-dialog::part(panel)", {
    borderRadius: "var(--sc-radius-large)",
    boxShadow: "var(--sc-elevation-4)"
});
globalStyle("mdui-dialog[fullscreen]:not([fullscreen=\"false\" i])::part(panel)", {
    borderRadius: "0",
    boxShadow: "none"
});
globalStyle("mdui-dialog::part(action)", {
    display: "flex",
    alignItems: "center",
    justifyContent: "flex-end",
    gap: "8px"
});
globalStyle("mdui-dialog span[slot=\"action\"],\nmdui-dialog [slot=\"action\"]", {
    display: "inline-flex",
    alignItems: "center",
    verticalAlign: "middle",
    gap: "8px"
});
globalStyle(".sc-admin-card mdui-text-field,\n.sc-settings-card mdui-text-field", {
    vars: { "--mdui-color-surface": "var(--mdui-color-surface-container-low)" }
});
globalStyle(".sc-field-error", {
    margin: "4px 16px 0",
    color: "rgb(var(--mdui-color-error))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-select-label", {
    margin: "0 0 6px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-label-medium-size)",
    fontWeight: "var(--mdui-typescale-label-medium-weight)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height)"
});
globalStyle(".sc-select select", {
    boxSizing: "border-box",
    inlineSize: "100%",
    minBlockSize: "48px",
    padding: "12px 40px 12px 16px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    backgroundColor: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface))",
    font: "inherit",
    appearance: "auto"
});
globalStyle(".sc-select select:hover", {
    backgroundColor: "rgb(var(--mdui-color-surface-container-high))"
});
globalStyle(".sc-select select:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-select select:disabled", {
    opacity: "0.38",
    cursor: "not-allowed"
});
globalStyle("mdui-chip", {
    maxInlineSize: "100%",
    minInlineSize: "0"
});
globalStyle("mdui-chip::part(label)", {
    minInlineSize: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-list-item", {
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    gap: "16px",
    minBlockSize: "56px",
    padding: "8px 16px",
    color: "rgb(var(--mdui-color-on-surface))"
});
globalStyle(".sc-list-item--clickable", {
    cursor: "pointer"
});
globalStyle(".sc-list-item--selected", {
    background: "rgb(var(--mdui-color-secondary-container))",
    color: "rgb(var(--mdui-color-on-secondary-container))"
});
globalStyle(".sc-list-item:has(:focus-visible)", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-inset-offset)"
});
globalStyle(".sc-list-item-leading", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    flex: "0 0 32px",
    inlineSize: "32px",
    blockSize: "32px"
});
globalStyle(".sc-list-item-text", {
    display: "flex",
    flex: "1 0 12rem",
    flexDirection: "column",
    minInlineSize: "0"
});
globalStyle(".sc-list-item-headline", {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "8px",
    minInlineSize: "0",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-body-large-size)",
    fontWeight: "var(--mdui-typescale-body-large-weight)",
    lineHeight: "var(--mdui-typescale-body-large-line-height)"
});
globalStyle(".sc-list-item-supporting", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-list-item-trailing", {
    display: "inline-flex",
    flexWrap: "wrap",
    alignItems: "center",
    justifyContent: "flex-end",
    gap: "8px",
    marginInlineStart: "auto",
    maxInlineSize: "100%"
});
globalStyle(".sc-menu-shell", {
    zIndex: "50",
    maxInlineSize: "calc(100vw - 16px)",
    maxBlockSize: "calc(100vh - 16px)",
    overflowY: "auto",
    transformOrigin: "top right"
});
globalStyle(".sc-menu-shell mdui-menu", {
    minInlineSize: "200px",
    maxInlineSize: "min(360px, calc(100vw - 16px))",
    borderRadius: "var(--sc-radius-medium)",
    overflow: "hidden",
    boxShadow: "var(--sc-elevation-3)"
});
globalStyle(".sc-menu-shell", {
    borderRadius: "var(--sc-radius-medium)",
    overflow: "hidden"
});
globalStyle("mdui-menu::part(panel),\nmdui-dropdown::part(panel),\nmdui-select::part(menu)", {
    borderRadius: "var(--sc-radius-medium)",
    overflow: "hidden"
});
globalStyle(".sc-sheet-scrim", {
    position: "fixed",
    inset: "0",
    zIndex: "49",
    background: "color-mix(in srgb, var(--mdui-color-scrim, #000) 36%, transparent)",
    animation: "sc-fade-in 150ms ease",
    WebkitTapHighlightColor: "transparent"
});
globalStyle(".sc-sheet", {
    position: "fixed",
    inset: "auto 0 0",
    zIndex: "50",
    boxSizing: "border-box",
    inlineSize: "100vw",
    maxInlineSize: "100vw",
    maxBlockSize: "80dvh",
    margin: "0",
    padding: "0",
    overflow: "hidden",
    border: "0",
    borderRadius: "var(--sc-radius-large) var(--sc-radius-large) 0 0",
    background: "rgb(var(--mdui-color-surface-container-low))",
    color: "rgb(var(--mdui-color-on-surface))",
    boxShadow: "var(--sc-elevation-3)",
    translate: "0 100%"
});
globalStyle(".sc-sheet[open]", {
    translate: "0 0"
});
globalStyle(".sc-sheet", {
    "@media": {
        "(prefers-reduced-motion: no-preference)": {
            transition: "translate 220ms cubic-bezier(0.2, 0, 0, 1),\n      overlay 220ms allow-discrete,\n      display 220ms allow-discrete"
        }
    }
});
globalStyle(".sc-sheet[open]", {
    "@media": {
        "(prefers-reduced-motion: no-preference)": {
            "@starting-style": {
                translate: "0 100%"
            }
        }
    }
});
globalStyle(".sc-sheet::backdrop", {
    background: "rgb(var(--mdui-color-scrim) / 0.32)"
});
globalStyle(".sc-sheet-handle-wrap", {
    display: "flex",
    justifyContent: "center",
    padding: "8px 0"
});
globalStyle(".sc-sheet-handle", {
    inlineSize: "32px",
    blockSize: "4px",
    borderRadius: "var(--sc-radius-full)",
    background: "rgb(var(--mdui-color-outline-variant))"
});
globalStyle(".sc-sheet-content", {
    display: "flex",
    flexDirection: "column",
    gap: "4px",
    padding: "8px 12px env(safe-area-inset-bottom, 0)",
    overflow: "auto"
});
globalStyle(".sc-sheet-content > *", {
    inlineSize: "100%"
});
globalStyle(".sc-sheet-content button", {
    minBlockSize: "48px",
    padding: "0 16px",
    border: "0",
    borderRadius: "var(--mdui-shape-corner-small)",
    background: "transparent",
    color: "inherit",
    font: "inherit",
    textAlign: "start",
    transition: "background-color 120ms ease, transform 100ms ease"
});
globalStyle(".sc-sheet-content button:hover", {
    background: "rgb(var(--mdui-color-surface-container))"
});
globalStyle(".sc-sheet-content button:active", {
    transform: "scale(0.98)"
});
globalStyle(".sc-breadcrumb", {
    display: "flex",
    flex: "1 1 auto",
    alignItems: "center",
    minWidth: "0",
    maxWidth: "100%",
    height: "40px",
    fontSize: "var(--mdui-typescale-label-large-size, .875rem)",
    fontWeight: "var(--mdui-typescale-label-large-weight, 500)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    letterSpacing: "var(--mdui-typescale-label-large-tracking, .006rem)"
});
globalStyle(".sc-breadcrumb-list", {
    display: "flex",
    flex: "1 1 auto",
    alignItems: "center",
    minWidth: "0",
    width: "100%",
    margin: "0",
    padding: "0",
    listStyle: "none"
});
globalStyle(".sc-breadcrumb-item", {
    display: "inline-flex",
    flex: "0 1 auto",
    alignItems: "center",
    minWidth: "0",
    height: "40px",
    whiteSpace: "nowrap"
});
globalStyle(".sc-breadcrumb-item--root", {
    maxWidth: "min(144px, 22%)"
});
globalStyle(".sc-breadcrumb-item--ancestor", {
    maxWidth: "min(160px, 18%)"
});
globalStyle(".sc-breadcrumb-item--parent", {
    maxWidth: "min(200px, 24%)"
});
globalStyle(".sc-breadcrumb-item--current", {
    maxWidth: "360px",
    flex: "1 1 0"
});
globalStyle(".sc-breadcrumb-item--ellipsis", {
    flex: "0 0 auto"
});
globalStyle(".sc-breadcrumb-link,\n.sc-breadcrumb-current", {
    display: "inline-flex",
    flex: "1 1 auto",
    alignItems: "center",
    minWidth: "0",
    height: "40px",
    boxSizing: "border-box",
    paddingInline: "8px",
    maxWidth: "100%",
    borderRadius: "var(--mdui-shape-corner-small, 8px)",
    font: "inherit",
    textAlign: "start"
});
globalStyle(".sc-breadcrumb-label", {
    display: "block",
    minWidth: "0",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-breadcrumb-link", {
    flex: "1 1 auto",
    minWidth: "0",
    width: "100%",
    border: "0",
    background: "transparent",
    color: "rgb(var(--mdui-color-primary))",
    cursor: "pointer",
    transition: "background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-breadcrumb-link:hover", {
    background: "rgb(var(--mdui-color-primary) / 0.08)"
});
globalStyle(".sc-breadcrumb-link:active", {
    background: "rgb(var(--mdui-color-primary) / 0.14)"
});
globalStyle(".sc-breadcrumb-link:focus-visible,\n.sc-breadcrumb-ellipsis-btn:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-breadcrumb-current", {
    color: "rgb(var(--mdui-color-on-surface))"
});
globalStyle(".sc-breadcrumb-ellipsis-btn", {
    display: "inline-flex",
    flex: "none",
    alignItems: "center",
    justifyContent: "center",
    width: "40px",
    height: "40px",
    padding: "0",
    border: "0",
    borderRadius: "var(--mdui-shape-corner-small, 8px)",
    background: "transparent",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    cursor: "pointer",
    font: "inherit",
    fontWeight: "700",
    letterSpacing: ".08em",
    transition: "background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-breadcrumb-ellipsis-btn:hover", {
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface))"
});
globalStyle(".sc-breadcrumb-sep", {
    display: "inline-flex",
    flex: "none",
    alignItems: "center",
    justifyContent: "center",
    height: "40px",
    paddingInline: "2px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    font: "inherit",
    opacity: ".55",
    userSelect: "none"
});
globalStyle(".sc-breadcrumb-menu", {
    boxSizing: "border-box",
    width: "max-content",
    minWidth: "200px",
    maxWidth: "min(320px, calc(100vw - 16px))"
});
globalStyle(".sc-breadcrumb-menu > button", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    width: "100%",
    minWidth: "0"
});
globalStyle(".sc-breadcrumb-menu > button > :first-child", {
    flex: "none"
});
globalStyle(".sc-breadcrumb-menu-label", {
    minWidth: "0",
    overflowWrap: "anywhere",
    whiteSpace: "normal"
});
globalStyle(".sc-breadcrumb,\n  .sc-breadcrumb-item,\n  .sc-breadcrumb-link,\n  .sc-breadcrumb-current,\n  .sc-breadcrumb-sep", {
    "@media": {
        "(max-width: 599.98px)": {
            height: "44px"
        }
    }
});
globalStyle(".sc-breadcrumb-ellipsis-btn", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "44px",
            height: "44px"
        }
    }
});
globalStyle(".sc-breadcrumb-item--root", {
    "@media": {
        "(max-width: 599.98px)": {
            maxWidth: "30%"
        }
    }
});
globalStyle(".sc-breadcrumb-link,\n  .sc-breadcrumb-current", {
    "@media": {
        "(max-width: 599.98px)": {
            paddingInline: "6px"
        }
    }
});
globalStyle(".sc-progress-linear", {
    display: "block",
    inlineSize: "100%"
});
globalStyle(".sc-progress-linear mdui-linear-progress", {
    display: "block",
    inlineSize: "100%"
});
globalStyle(".sc-progress-linear--weak", {
    vars: { "--mdui-color-primary": "var(--mdui-color-error)" }
});
globalStyle(".sc-progress-linear--fair", {
    vars: { "--mdui-color-primary": "var(--mdui-color-tertiary)" }
});
globalStyle(".sc-progress-linear--strong", {
    vars: { "--mdui-color-primary": "var(--mdui-color-primary)" }
});
globalStyle("mdui-snackbar", {
    vars: { "--shape-corner": "var(--mdui-shape-corner-extra-small)" }
});
globalStyle(".sc-list-item", {
    "@media": {
        "(max-width: 599px)": {
            gap: "12px",
            paddingInline: "12px"
        }
    }
});
globalStyle(".sc-list-item-text", {
    "@media": {
        "(max-width: 599px)": {
            flexBasis: "10rem"
        }
    }
});
globalStyle(".sc-select select", {
    "@media": {
        "(max-width: 599px)": {
            minBlockSize: "52px"
        }
    }
});
