import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-admin-section", {
    marginBlock: "0"
});
globalStyle(".sc-admin-section > h3", {
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-title-large-size)",
    fontWeight: "var(--mdui-typescale-title-large-weight)",
    letterSpacing: "var(--mdui-typescale-title-large-tracking)",
    lineHeight: "var(--mdui-typescale-title-large-line-height)"
});
globalStyle(".sc-admin-section-subhead", {
    margin: "32px 0 8px",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-title-small-size)",
    lineHeight: "var(--mdui-typescale-title-small-line-height)"
});
globalStyle(".sc-admin-section-hint", {
    maxWidth: "40rem",
    marginBottom: "16px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-section-warning", {
    marginBottom: "16px",
    padding: "12px 16px",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-error-container))",
    color: "rgb(var(--mdui-color-on-error-container))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-section-error", {
    margin: "8px 0 0",
    color: "rgb(var(--mdui-color-error))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-section-status", {
    margin: "8px 0 0",
    color: "rgb(var(--mdui-color-primary))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-section-status-error", {
    color: "rgb(var(--mdui-color-error))"
});
globalStyle(".sc-server-settings-nav", {
    position: "sticky",
    top: "0",
    zIndex: "2",
    margin: "0 0 20px",
    padding: "8px 0",
    background: "color-mix(in srgb, var(--sc-page-surface) 95%, transparent)",
    backdropFilter: "blur(12px)"
});
globalStyle(".sc-server-settings-nav-items", {
    display: "flex",
    gap: "8px",
    overflowX: "auto",
    padding: "2px",
    scrollbarWidth: "none"
});
globalStyle(".sc-server-settings-nav-items::-webkit-scrollbar", {
    display: "none"
});
globalStyle(".sc-server-settings-nav button", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    flex: "none",
    minBlockSize: "var(--sc-control-min)",
    padding: "0 16px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-full)",
    color: "rgb(var(--mdui-color-on-surface))",
    background: "rgb(var(--mdui-color-surface-container-high))",
    cursor: "pointer",
    whiteSpace: "nowrap",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    fontWeight: "500",
    transition: "background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-server-settings-nav button:hover", {
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-server-settings-nav button:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-server-settings-form", {
    display: "flex",
    flexDirection: "column",
    alignItems: "flex-start",
    gap: "16px",
    width: "100%",
    maxWidth: "32rem",
    minWidth: "0"
});
globalStyle(".sc-server-settings-form > .sc-field, .sc-server-settings-form > .sc-select, .sc-server-settings-form .sc-field", {
    width: "100%"
});
globalStyle(".sc-server-settings-form > label:not(.sc-switch-row),\n.sc-server-settings-form label:not(.sc-switch-row)", {
    display: "flex",
    flexDirection: "column",
    gap: "6px",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    color: "rgb(var(--mdui-color-on-surface))",
    width: "100%"
});
globalStyle(".sc-server-settings-form select", {
    minBlockSize: "48px",
    width: "100%",
    maxWidth: "24rem",
    padding: "10px 16px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-body-large-size)",
    cursor: "pointer"
});
globalStyle(".sc-server-settings-form select:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-server-settings-switch", {
    display: "flex",
    alignItems: "center",
    gap: "12px",
    minBlockSize: "48px"
});
globalStyle(".sc-server-settings-path-row", {
    display: "flex",
    gap: "8px",
    alignItems: "center",
    width: "100%",
    minWidth: "0"
});
globalStyle(".sc-server-settings-path-row .sc-field", {
    width: "auto",
    flex: "1 1 auto",
    minWidth: "0"
});
globalStyle(".sc-server-settings-other", {
    display: "flex",
    flexDirection: "column",
    gap: "12px",
    margin: "0 0 16px"
});
globalStyle(".sc-server-settings-other dt", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontFamily: "ui-monospace, monospace",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-server-settings-other dd", {
    margin: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-server-settings-reason, .sc-server-settings-empty-note", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-server-settings-empty-note", {
    margin: "0"
});
globalStyle(".sc-server-settings-findings", {
    display: "grid",
    gap: "4px",
    margin: "8px 0 0",
    padding: "0",
    listStyle: "none"
});
globalStyle(".sc-server-settings-finding", {
    margin: "0",
    padding: "8px 12px",
    borderInlineStart: "4px solid rgb(var(--mdui-color-outline))",
    borderRadius: "0",
    background: "transparent",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-server-settings-finding-block", {
    borderInlineStartColor: "rgb(var(--mdui-color-error))"
});
globalStyle(".sc-server-settings-finding-ok", {
    borderInlineStartColor: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-server-settings-finding strong, .sc-server-settings-finding code", {
    marginInlineEnd: "4px"
});
globalStyle(".sc-server-settings-finding code, .sc-server-settings-agent-detail", {
    fontFamily: "ui-monospace, monospace"
});
globalStyle(".sc-server-settings-agent-detail", {
    margin: "8px 0 0",
    whiteSpace: "pre-wrap",
    overflowWrap: "anywhere"
});
globalStyle(".sc-server-settings-endpoints", {
    display: "flex",
    flexDirection: "column",
    gap: "4px",
    width: "100%",
    marginTop: "8px"
});
globalStyle(".sc-server-settings-endpoint-row", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    width: "100%",
    minWidth: "0"
});
globalStyle(".sc-server-settings-endpoint-uri", {
    flex: "1",
    minWidth: "0",
    fontFamily: "ui-monospace, monospace",
    overflowWrap: "anywhere",
    userSelect: "all"
});
globalStyle(".sc-server-settings-announce", {
    minBlockSize: "1.25em",
    margin: "0",
    color: "rgb(var(--mdui-color-primary))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-server-settings-nav button", {
    "@media": {
        "(max-width: 599.98px)": {
            minBlockSize: "44px"
        }
    }
});
globalStyle(".sc-server-settings-path-row", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "stretch",
            flexDirection: "column"
        }
    }
});
globalStyle(".sc-server-settings-path-row > .sc-button-wrap", {
    "@media": {
        "(max-width: 599.98px)": {
            alignSelf: "flex-start"
        }
    }
});
globalStyle(".sc-server-settings-endpoint-row", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "flex-start"
        }
    }
});
