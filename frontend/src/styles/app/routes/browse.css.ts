import { globalKeyframes, globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-browse-context", {
    position: "fixed",
    zIndex: "100",
    display: "grid",
    minWidth: "200px",
    padding: "8px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-medium, 12px)",
    background: "rgb(var(--mdui-color-surface-container-high))",
    boxShadow: "0 8px 24px rgba(0, 0, 0, 0.28)",
    animation: "sc-scale-up 150ms cubic-bezier(0.2, 0, 0, 1)",
    transformOrigin: "top left"
});
globalStyle(".sc-browse-context button, .sc-browse-new-menu button", {
    minHeight: "var(--sc-control-min)",
    padding: "8px 12px",
    border: "0",
    borderRadius: "var(--mdui-shape-corner-extra-small, 4px)",
    background: "transparent",
    color: "inherit",
    textAlign: "start",
    cursor: "pointer",
    transition: "background-color 120ms ease, transform 100ms ease"
});
globalStyle(".sc-browse-context button:active, .sc-browse-new-menu button:active", {
    transform: "scale(0.98)"
});
globalStyle(".sc-browse-context button:hover, .sc-browse-context button:focus-visible, .sc-browse-new-menu button:hover, .sc-browse-new-menu button:focus-visible", {
    background: "color-mix(in srgb, currentColor 10%, transparent)"
});
globalStyle(".sc-browse-context button:focus-visible,\n.sc-browse-new-menu button:focus-visible,\n.sc-browse-filter-pill:focus-visible,\n.sc-browse-action-btn:focus-visible,\n.sc-browse-fab-btn:focus-visible,\n.sc-browse-operation-close:focus-visible,\n.sc-browse-selection-close-btn:focus-visible,\n.sc-browse-selection-action-btn:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-browse-new-menu", {
    display: "grid",
    minWidth: "200px",
    padding: "8px",
    animation: "sc-scale-up 150ms cubic-bezier(0.2, 0, 0, 1)",
    transformOrigin: "top left"
});
globalStyle(".sc-snackbar", {
    position: "fixed",
    left: "50%",
    bottom: "24px",
    transform: "translateX(-50%)",
    zIndex: "120",
    maxWidth: "calc(100vw - 32px)",
    boxSizing: "border-box",
    padding: "12px 16px",
    borderRadius: "var(--mdui-shape-corner-small, 8px)",
    background: "var(--mdui-color-inverse-surface, var(--m3c-inverse-surface))",
    color: "var(--mdui-color-inverse-on-surface, var(--m3c-inverse-on-surface))",
    fontSize: "var(--mdui-typescale-body-medium-size, .875rem)",
    boxShadow: "0 4px 16px rgba(0, 0, 0, 0.28)",
    animation: "sc-snackbar-enter 200ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-app-shell--compact .sc-snackbar", {
    bottom: "max(calc(16px + var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px)), var(--sc-tray-stack-top, 0px))"
});
globalKeyframes("sc-snackbar-enter", {
    "from": {
        opacity: "0",
        transform: "translate(-50%, 12px) scale(0.96)"
    },
    "to": {
        opacity: "1",
        transform: "translate(-50%, 0) scale(1)"
    }
});
globalStyle(".sc-browse", {
    display: "flex",
    flexDirection: "column",
    height: "100%",
    minHeight: "0",
    position: "relative",
    background: "var(--sc-page-surface, var(--mdui-color-surface, var(--m3c-surface)))"
});
globalStyle(".sc-app-shell--compact .sc-browse", {
    flex: "1",
    height: "auto"
});
globalStyle(".sc-browse-toolbar", {
    display: "flex",
    flexWrap: "wrap",
    flex: "0 0 auto",
    alignItems: "center",
    justifyContent: "space-between",
    gap: "8px 16px",
    width: "100%",
    minHeight: "52px",
    padding: "12px var(--sc-content-pad)",
    boxSizing: "border-box",
    borderBottom: "none",
    background: "transparent"
});
globalStyle(".sc-browse-toolbar--compact", {
    padding: "10px 16px",
    gap: "8px",
    vars: { "--sc-content-pad": "16px" }
});
globalStyle(".sc-browse-folder-heading", {
    display: "flex",
    flexWrap: "wrap",
    flex: "1 1 auto",
    alignItems: "center",
    gap: "8px 12px",
    minWidth: "0",
    maxWidth: "100%"
});
globalStyle(".sc-browse-folder-heading > .sc-breadcrumb", {
    flexBasis: "auto"
});
globalStyle(".sc-browse-toolbar--compact .sc-browse-folder-heading", {
    flexBasis: "auto"
});
globalStyle(".sc-breadcrumb-item--root .sc-breadcrumb-label", {
    color: "rgb(var(--mdui-color-primary))",
    fontWeight: "600"
});
globalStyle(".sc-browse-toolbar-actions", {
    display: "flex",
    alignItems: "center",
    justifyContent: "flex-end",
    gap: "8px",
    minWidth: "0",
    maxWidth: "100%",
    flex: "0 0 auto",
    marginInlineStart: "auto"
});
globalStyle(".sc-browse-filter-pill", {
    minHeight: "var(--sc-control-min)",
    padding: "0 12px",
    borderRadius: "10px",
    background: "color-mix(in srgb, var(--sc-content-primary) 5%, transparent)",
    border: "1px solid var(--sc-outline-variant)",
    color: "var(--sc-content-secondary)",
    fontFamily: "inherit",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    fontWeight: "500",
    display: "inline-flex",
    alignItems: "center",
    gap: "4px",
    cursor: "pointer",
    transition: "background-color 140ms ease, border-color 140ms ease, color 140ms ease"
});
globalStyle(".sc-browse-filter-pill:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 10%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-browse-filter-pill--active", {
    background: "var(--sc-state-selection)",
    borderColor: "color-mix(in srgb, var(--sc-state-selection-content) 35%, transparent)",
    color: "var(--sc-state-selection-content)",
    fontWeight: "600"
});
globalStyle(".sc-browse-action-btn", {
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    borderRadius: "10px",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-secondary)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    cursor: "pointer",
    transition: "background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-browse-action-btn:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 8%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-browse-action-btn.is-active", {
    color: "var(--sc-state-selection-content)",
    background: "var(--sc-state-selection)"
});
globalStyle(".sc-browse-fab-btn", {
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    borderRadius: "12px",
    border: "none",
    background: "rgb(var(--mdui-color-primary))",
    color: "rgb(var(--mdui-color-on-primary))",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    cursor: "pointer",
    boxShadow: "0 2px 6px rgba(var(--mdui-color-primary), 0.22)"
});
globalStyle(".sc-browse-fab-btn:hover", {
    background: "color-mix(in srgb, rgb(var(--mdui-color-primary)) 88%, var(--sc-content-primary))"
});
globalStyle(".sc-browse-operation", {
    margin: "12px var(--sc-page-pad, 32px)",
    padding: "16px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-medium, 12px)",
    background: "rgb(var(--mdui-color-surface-container))",
    boxShadow: "0 4px 16px rgba(0, 0, 0, 0.12)",
    animation: "sc-fade-in-up 200ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-browse-operation-heading", {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: "12px"
});
globalStyle(".sc-browse-operation-heading h2", {
    margin: "0",
    fontSize: "var(--mdui-typescale-title-medium-size, 1rem)",
    fontWeight: "var(--mdui-typescale-title-medium-weight, 500)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height, 1.5rem)"
});
globalStyle(".sc-browse-operation-close", {
    minHeight: "var(--sc-control-min)",
    border: "0",
    padding: "4px 12px",
    borderRadius: "20px",
    color: "var(--mdui-color-primary, var(--m3c-primary))",
    background: "transparent",
    cursor: "pointer",
    font: "inherit",
    transition: "background-color 140ms ease, transform 100ms ease"
});
globalStyle(".sc-browse-operation-close:active", {
    transform: "scale(0.96)"
});
globalStyle(".sc-browse-operation ul", {
    display: "grid",
    gap: "8px",
    margin: "12px 0 0",
    padding: "0",
    listStyle: "none"
});
globalStyle(".sc-browse-operation li", {
    display: "flex",
    flexWrap: "wrap",
    gap: "4px 12px",
    justifyContent: "space-between",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-body-small-size, .875rem)",
    lineHeight: "var(--mdui-typescale-body-small-line-height, 1.25rem)"
});
globalStyle(".sc-browse-operation-error", {
    color: "var(--mdui-color-error, var(--m3c-error))"
});
globalStyle(".sc-browse-operation-path", {
    minWidth: "0"
});
globalStyle(".sc-browse-operation-jobs", {
    margin: "8px 0 0",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-body-small-size, .875rem)"
});
globalStyle(".sc-browse-external-badge, .sc-browse-broken-badge, .sc-browse-encrypted-badge", {
    display: "inline-flex",
    alignItems: "center",
    gap: "4px",
    minHeight: "24px",
    maxWidth: "100%",
    boxSizing: "border-box",
    paddingInline: "8px",
    borderRadius: "var(--mdui-shape-corner-full, 999px)",
    fontSize: "var(--mdui-typescale-label-small-size, .6875rem)",
    lineHeight: "var(--mdui-typescale-label-small-line-height, 1rem)",
    overflowWrap: "anywhere",
    transition: "background-color 150ms ease, color 150ms ease, transform 120ms ease",
    flexShrink: "0"
});
globalStyle(".sc-browse-external-badge", {
    background: "var(--sc-state-warning, var(--mdui-color-tertiary-container, var(--m3c-tertiary-container)))",
    color: "var(--sc-state-warning-content, var(--mdui-color-on-tertiary-container, var(--m3c-on-tertiary-container)))"
});
globalStyle(".sc-browse-broken-badge", {
    background: "var(--sc-state-error, var(--mdui-color-error-container, var(--m3c-error-container)))",
    color: "var(--sc-state-error-content, var(--mdui-color-on-error-container, var(--m3c-on-error-container)))"
});
globalStyle(".sc-browse-encrypted-badge", {
    background: "var(--mdui-color-surface-container-highest, var(--m3c-surface-container-highest))",
    color: "var(--sc-content-secondary, var(--mdui-color-on-surface-variant, var(--m3c-on-surface-variant)))"
});
globalStyle(".sc-browse-encrypted-badge--locked", {
    minHeight: "var(--sc-control-min)",
    paddingInline: "12px",
    border: "none",
    cursor: "pointer",
    background: "var(--mdui-color-secondary-container, var(--m3c-secondary-container))",
    color: "var(--mdui-color-on-secondary-container, var(--m3c-on-secondary-container))"
});
globalStyle(".sc-browse-encrypted-badge--locked:active", {
    transform: "scale(0.95)"
});
globalStyle(".sc-browse-encrypted-badge--locked:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-browse-table-wrap--marquee", {
    userSelect: "none",
    cursor: "crosshair"
});
globalStyle(".sc-browse-marquee", {
    position: "fixed",
    zIndex: "15",
    pointerEvents: "none",
    border: "1px solid var(--sc-state-focus, var(--mdui-color-primary, var(--m3c-primary)))",
    background: "color-mix(in srgb, var(--mdui-color-primary, var(--m3c-primary)) 18%, transparent)",
    borderRadius: "2px"
});
globalStyle(".sc-browse-selection-bar", {
    position: "fixed",
    left: "50%",
    transform: "translateX(-50%)",
    bottom: "calc(24px + var(--sc-selection-bar-offset, 0px) + env(safe-area-inset-bottom, 0px))",
    zIndex: "25",
    maxWidth: "calc(100vw - 32px)",
    borderRadius: "28px",
    background: "var(--sc-overlay-surface)",
    border: "1px solid var(--sc-outline-variant)",
    boxShadow: "0 8px 32px rgba(0, 0, 0, 0.32)",
    animation: "sc-selection-bar-enter 180ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-app-shell--compact .sc-browse-selection-bar", {
    bottom: "max(\n    calc(16px + var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px)),\n    var(--sc-tray-stack-top, 0px)\n  )"
});
globalStyle(".sc-browse-selection-bar-inner", {
    display: "flex",
    alignItems: "center",
    gap: "10px",
    height: "48px",
    padding: "0 14px 0 10px",
    whiteSpace: "nowrap"
});
globalStyle(".sc-browse-selection-close-btn", {
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    borderRadius: "50%",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-secondary)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    cursor: "pointer",
    padding: "0",
    transition: "background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-browse-selection-close-btn:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 10%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-browse-selection-count", {
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height)",
    fontWeight: "500",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-browse-selection-divider", {
    width: "1px",
    height: "18px",
    background: "var(--sc-outline-variant)",
    margin: "0 2px",
    flex: "none"
});
globalStyle(".sc-browse-selection-actions", {
    display: "flex",
    alignItems: "center",
    gap: "2px"
});
globalStyle(".sc-browse-selection-action-btn", {
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    borderRadius: "50%",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-secondary)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    cursor: "pointer",
    padding: "0",
    transition: "background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-browse-selection-action-btn:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 10%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-browse-content", {
    display: "flex",
    flex: "1",
    minHeight: "0",
    containerType: "inline-size",
    containerName: "sc-browse-content"
});
globalStyle(".sc-browse-table-wrap", {
    position: "relative",
    flex: "1",
    display: "flex",
    minWidth: "0",
    minHeight: "0"
});
globalStyle(".sc-browse-table-wrap--dragover", {
    outline: "2px dashed var(--mdui-color-primary, var(--m3c-primary))",
    outlineOffset: "-2px"
});
globalStyle(".sc-browse-view", {
    position: "absolute",
    inset: "0",
    display: "flex",
    minWidth: "0",
    minHeight: "0"
});
globalStyle(".sc-browse-loading", {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    flex: "1",
    minHeight: "240px"
});
globalStyle(".sc-browse-nothing", {
    display: "flex",
    flex: "1",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    gap: "16px",
    padding: "48px 24px",
    textAlign: "center",
    animation: "sc-fade-in-up 220ms cubic-bezier(0.2,0,0,1)"
});
globalStyle(".sc-browse-nothing-icon", {
    width: "72px",
    height: "72px",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    borderRadius: "24px",
    background: "var(--sc-state-selection)",
    color: "var(--sc-state-selection-content)",
    fontSize: "36px",
    lineHeight: "1",
    boxShadow: "var(--sc-shadow-card, 0 1px 2px rgba(0,0,0,.06))"
});
globalStyle(".sc-browse-nothing-title", {
    margin: "0",
    fontSize: "var(--mdui-typescale-headline-small-size, 1.5rem)",
    fontWeight: "var(--mdui-typescale-headline-small-weight, 400)",
    lineHeight: "var(--mdui-typescale-headline-small-line-height, 2rem)"
});
globalStyle(".sc-browse-nothing-hint", {
    maxWidth: "360px",
    margin: "0",
    color: "var(--sc-content-secondary, var(--mdui-color-on-surface-variant, var(--m3c-on-surface-variant)))",
    fontSize: "var(--mdui-typescale-body-medium-size, .875rem)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height, 1.25rem)"
});
globalStyle(".sc-browse-error", {
    padding: "24px",
    color: "var(--mdui-color-error, var(--m3c-error))"
});
globalStyle(".sc-browse-drop-overlay", {
    position: "absolute",
    inset: "0",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    background: "color-mix(in srgb, var(--mdui-color-primary, var(--m3c-primary)) 12%, transparent)",
    color: "var(--mdui-color-primary, var(--m3c-primary))",
    fontSize: "var(--mdui-typescale-title-medium-size, 1rem)",
    fontWeight: "var(--mdui-typescale-title-medium-weight, 500)",
    pointerEvents: "none"
});
globalStyle(".sc-preview-dialog", {
    padding: "max(16px, env(safe-area-inset-top, 0px)) max(16px, env(safe-area-inset-right, 0px)) max(16px, env(safe-area-inset-bottom, 0px)) max(16px, env(safe-area-inset-left, 0px))",
    boxSizing: "border-box",
    vars: { "--shape-corner": "var(--mdui-shape-corner-extra-large)" }
});
globalStyle(".sc-preview-dialog::part(panel)", {
    width: "min(75rem, 100%)",
    height: "100%",
    minWidth: "0",
    maxWidth: "100%",
    maxHeight: "56rem",
    padding: "0",
    overflow: "hidden"
});
globalStyle(".sc-preview-dialog::part(body)", {
    display: "flex",
    flex: "1",
    minHeight: "0",
    margin: "0",
    overflow: "hidden"
});
globalStyle(".sc-preview", {
    display: "flex",
    flex: "1",
    flexDirection: "column",
    minWidth: "0",
    minHeight: "0",
    background: "rgb(var(--mdui-color-surface-container))",
    color: "rgb(var(--mdui-color-on-surface))"
});
globalStyle(".sc-preview-bar", {
    display: "flex",
    flex: "none",
    alignItems: "center",
    gap: "8px",
    padding: "16px",
    background: "rgb(var(--mdui-color-surface-container-high))"
});
globalStyle(".sc-preview .sc-icon-button mdui-button-icon", {
    inlineSize: "44px",
    blockSize: "44px"
});
globalStyle(".sc-preview-bar .sc-icon-button, .sc-preview-nav .sc-icon-button", {
    borderRadius: "var(--mdui-shape-corner-full)",
    background: "rgb(var(--mdui-color-surface-container-highest))"
});
globalStyle(".sc-preview-meta", {
    display: "flex",
    flex: "1",
    flexDirection: "column",
    gap: "2px",
    minWidth: "0"
});
globalStyle(".sc-preview-name", {
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    fontSize: "var(--mdui-typescale-title-medium-size, 1rem)",
    fontWeight: "500"
});
globalStyle(".sc-preview-size", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-preview-body", {
    display: "flex",
    flex: "1",
    minWidth: "0",
    minHeight: "0",
    padding: "16px"
});
globalStyle(".sc-preview-nav", {
    display: "flex",
    flex: "none",
    justifyContent: "space-between",
    gap: "8px",
    padding: "0 16px 16px"
});
globalStyle(".sc-preview-stage", {
    display: "flex",
    flex: "1",
    alignItems: "center",
    justifyContent: "center",
    minWidth: "0",
    minHeight: "0",
    overflow: "auto",
    borderRadius: "var(--mdui-shape-corner-large)",
    background: "rgb(var(--mdui-color-surface-container-low))"
});
globalStyle(".sc-preview-video-container", {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    width: "100%",
    height: "100%",
    overflow: "hidden"
});
globalStyle(".sc-preview-video", {
    maxWidth: "100%",
    maxHeight: "100%",
    borderRadius: "8px",
    boxShadow: "0 4px 24px rgb(0 0 0 / 40%)"
});
globalStyle(".sc-preview-image", {
    maxWidth: "100%",
    maxHeight: "100%",
    objectFit: "contain"
});
globalStyle(".sc-preview-text", {
    width: "100%",
    height: "100%",
    margin: "0",
    padding: "16px",
    boxSizing: "border-box",
    overflow: "auto",
    whiteSpace: "pre-wrap",
    overflowWrap: "anywhere",
    color: "inherit",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    lineHeight: "1.6"
});
globalStyle(".sc-preview-card", {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    gap: "8px",
    width: "min(480px, 100%)",
    padding: "24px",
    boxSizing: "border-box",
    borderRadius: "var(--mdui-shape-corner-large)",
    background: "rgb(var(--mdui-color-surface-container))",
    color: "inherit",
    textAlign: "center"
});
globalStyle(".sc-preview-card-title", {
    margin: "0",
    fontSize: "var(--mdui-typescale-title-medium-size)",
    fontWeight: "var(--mdui-typescale-title-medium-weight, 500)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height)"
});
globalStyle(".sc-preview-card-reason, .sc-preview-card-detail", {
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-preview-card-detail", {
    overflowWrap: "anywhere",
    fontFamily: "ui-monospace, monospace",
    fontSize: ".8rem"
});
globalStyle(".sc-preview-card-actions", {
    display: "flex",
    flexWrap: "wrap",
    justifyContent: "center",
    gap: "12px",
    marginTop: "16px"
});
globalStyle(".sc-preview-archive", {
    display: "flex",
    flexDirection: "column",
    gap: "8px",
    width: "100%",
    maxWidth: "720px",
    maxHeight: "100%",
    padding: "16px",
    boxSizing: "border-box",
    overflow: "auto",
    color: "inherit"
});
globalStyle(".sc-preview-archive-count, .sc-preview-archive-empty", {
    margin: "0",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-preview-archive-list", {
    margin: "0",
    padding: "0",
    listStyle: "none"
});
globalStyle(".sc-preview-archive-row", {
    display: "grid",
    gridTemplateColumns: "auto 1fr auto auto",
    gap: "12px",
    alignItems: "center",
    width: "100%",
    padding: "8px 0",
    border: "0",
    borderBottom: "1px solid rgb(var(--mdui-color-outline-variant))",
    background: "none",
    color: "inherit",
    font: "inherit",
    textAlign: "start"
});
globalStyle("button.sc-preview-archive-row", {
    cursor: "pointer"
});
globalStyle(".sc-preview-archive-row:hover", {
    background: "rgb(var(--mdui-color-surface-container-highest))"
});
globalStyle(".sc-preview-archive-row--up", {
    gridTemplateColumns: "auto 1fr"
});
globalStyle(".sc-preview-archive-name", {
    minWidth: "0",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-preview-archive-skipped", {
    color: "rgb(var(--mdui-color-error))"
});
globalStyle(".sc-preview-crumbs", {
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    gap: "4px",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-preview-crumb", {
    maxWidth: "240px",
    overflow: "hidden",
    padding: "4px",
    border: "0",
    borderRadius: "4px",
    background: "none",
    color: "inherit",
    font: "inherit",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    cursor: "pointer"
});
globalStyle(".sc-preview-crumb:hover:not(:disabled)", {
    background: "rgb(var(--mdui-color-surface-container-highest))"
});
globalStyle(".sc-preview-crumb:disabled", {
    cursor: "default",
    opacity: ".75"
});
globalStyle(".sc-preview-crumb-sep", {
    opacity: ".5"
});
globalStyle(".sc-preview-crumb:focus-visible, .sc-preview-archive-row:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-preview-dialog", {
    "@media": {
        "(min-width: 840px)": {
            padding: "32px"
        }
    }
});
