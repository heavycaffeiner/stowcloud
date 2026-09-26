import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-logs", {
    minWidth: "0",
    containerName: "sc-logs",
    containerType: "inline-size"
});
globalStyle(".sc-logs h2", {
    margin: "0 0 8px",
    fontSize: "var(--mdui-typescale-title-large-size)",
    fontWeight: "var(--mdui-typescale-title-large-weight)",
    lineHeight: "var(--mdui-typescale-title-large-line-height)"
});
globalStyle(".sc-logs h3", {
    margin: "0",
    fontSize: "var(--mdui-typescale-title-medium-size)",
    fontWeight: "var(--mdui-typescale-title-medium-weight)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height)"
});
globalStyle(".sc-logs-hint", {
    maxWidth: "40rem",
    margin: "0 0 16px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-logs-figures", {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(140px, 1fr))",
    gap: "16px",
    maxWidth: "30rem",
    margin: "0 0 24px"
});
globalStyle(".sc-logs-figures div", {
    display: "flex",
    flexDirection: "column",
    gap: "4px"
});
globalStyle(".sc-logs-figures dt", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-figures dd", {
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-title-medium-size)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height)"
});
globalStyle(".sc-logs-filters", {
    display: "flex",
    flexDirection: "column",
    alignItems: "flex-start",
    gap: "16px",
    marginBottom: "24px"
});
globalStyle(".sc-logs-sources", {
    display: "flex",
    flexDirection: "column",
    gap: "8px"
});
globalStyle(".sc-logs-group-label, .sc-logs-levels legend", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-source-button", {
    minBlockSize: "var(--sc-control-min)",
    paddingInline: "12px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-full)",
    background: "rgb(var(--mdui-color-surface-container-high))",
    color: "inherit",
    cursor: "pointer",
    transition: "background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-logs-source-button:hover", {
    background: "rgb(var(--mdui-color-surface-container-highest))"
});
globalStyle(".sc-logs-source-button-active", {
    background: "rgb(var(--mdui-color-primary-container))",
    color: "rgb(var(--mdui-color-on-primary-container))"
});
globalStyle(".sc-logs-source-button:focus-visible,\n.sc-logs-bar:focus-visible,\n.sc-logs-row-button:focus-visible,\n.sc-logs-table-wrap summary:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-logs-levels", {
    margin: "0",
    padding: "0",
    border: "0"
});
globalStyle(".sc-logs-levels legend", {
    padding: "0",
    marginBottom: "8px"
});
globalStyle(".sc-logs-level-boxes", {
    display: "flex",
    flexWrap: "wrap",
    gap: "16px"
});
globalStyle(".sc-logs-check", {
    display: "inline-flex",
    alignItems: "center",
    gap: "6px",
    minBlockSize: "var(--sc-control-min)"
});
globalStyle(".sc-logs-fields", {
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    gap: "16px",
    width: "100%"
});
globalStyle(".sc-logs-fields > .sc-field", {
    flex: "1 1 180px",
    maxWidth: "280px"
});
globalStyle(".sc-logs-auto-note, .sc-logs-scope, .sc-logs-warn", {
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-scope, .sc-logs-warn", {
    display: "flex",
    alignItems: "flex-start",
    gap: "8px",
    maxWidth: "40rem"
});
globalStyle(".sc-logs-warn", {
    padding: "8px 12px",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-tertiary-container))",
    color: "rgb(var(--mdui-color-on-tertiary-container))"
});
globalStyle(".sc-logs-chart", {
    marginBottom: "24px"
});
globalStyle(".sc-logs-chart-head", {
    marginBottom: "16px"
});
globalStyle(".sc-logs-chart-head .sc-logs-hint", {
    margin: "4px 0 0"
});
globalStyle(".sc-logs-legend", {
    display: "flex",
    flexWrap: "wrap",
    gap: "8px 16px",
    margin: "0 0 12px",
    padding: "0",
    listStyle: "none"
});
globalStyle(".sc-logs-legend li", {
    display: "inline-flex",
    alignItems: "center",
    gap: "4px",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-label-medium-size)"
});
globalStyle(".sc-logs-legend-source", {
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-logs-swatch", {
    display: "inline-block",
    inlineSize: "16px",
    blockSize: "16px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-extra-small)"
});
globalStyle(".sc-logs-plot", {
    display: "flex",
    alignItems: "flex-end",
    gap: "4px",
    blockSize: "160px",
    minWidth: "0",
    padding: "8px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-medium)",
    background: "rgb(var(--mdui-color-surface-container-lowest))",
    boxShadow: "inset 0 1px 3px rgba(0, 0, 0, 0.2)",
    overflow: "hidden"
});
globalStyle(".sc-logs-bar", {
    display: "flex",
    alignItems: "flex-end",
    flex: "1 1 0",
    minInlineSize: "0",
    blockSize: "100%",
    padding: "0",
    border: "0",
    borderRadius: "2px",
    background: "none",
    cursor: "pointer"
});
globalStyle(".sc-logs-bar-active", {
    background: "rgb(var(--mdui-color-surface-container-high))",
    outline: "1px solid rgb(var(--mdui-color-outline))"
});
globalStyle(".sc-logs-stack", {
    display: "flex",
    flexDirection: "column-reverse",
    justifyContent: "flex-start",
    inlineSize: "100%",
    blockSize: "100%"
});
globalStyle(".sc-logs-seg", {
    flexShrink: "0",
    inlineSize: "100%",
    minBlockSize: "1px"
});
globalStyle(".sc-logs-baseline", {
    inlineSize: "100%",
    blockSize: "2px",
    background: "rgb(var(--mdui-color-outline-variant))"
});
globalStyle(".sc-logs-seg, .sc-logs-swatch", {
    background: "rgb(var(--mdui-color-outline))"
});
globalStyle(".sc-logs-seg-server-debug", {
    background: "rgb(var(--mdui-color-surface-container-highest))"
});
globalStyle(".sc-logs-seg-server-info", {
    background: "rgb(var(--mdui-color-secondary-container))",
    backgroundImage: "repeating-linear-gradient(45deg, transparent 0 3px, rgb(var(--mdui-color-on-secondary-container)) 3px 4px)"
});
globalStyle(".sc-logs-seg-server-warn", {
    background: "rgb(var(--mdui-color-tertiary-container))",
    backgroundImage: "repeating-linear-gradient(-45deg, transparent 0 3px, rgb(var(--mdui-color-on-tertiary-container)) 3px 4px)"
});
globalStyle(".sc-logs-seg-server-error", {
    background: "rgb(var(--mdui-color-error-container))",
    backgroundImage: "repeating-linear-gradient(90deg, transparent 0 2px, rgb(var(--mdui-color-on-error-container)) 2px 4px)"
});
globalStyle(".sc-logs-seg-audit-ok", {
    background: "rgb(var(--mdui-color-primary-container))",
    backgroundImage: "repeating-linear-gradient(0deg, transparent 0 3px, rgb(var(--mdui-color-on-primary-container)) 3px 4px)"
});
globalStyle(".sc-logs-seg-audit-failed", {
    background: "rgb(var(--mdui-color-error-container))",
    backgroundImage: "repeating-linear-gradient(45deg, transparent 0 3px, rgb(var(--mdui-color-on-error-container)) 3px 4px), repeating-linear-gradient(-45deg, transparent 0 3px, rgb(var(--mdui-color-on-error-container)) 3px 4px)"
});
globalStyle(".sc-logs-axis", {
    display: "flex",
    justifyContent: "space-between",
    gap: "8px",
    marginTop: "8px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-readout", {
    margin: "8px 0 0",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-table-wrap", {
    minWidth: "0",
    marginTop: "12px"
});
globalStyle(".sc-logs-table-wrap summary", {
    display: "flex",
    alignItems: "center",
    width: "fit-content",
    minBlockSize: "var(--sc-control-min)",
    padding: "4px 0",
    color: "rgb(var(--mdui-color-primary))",
    cursor: "pointer",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-table-scroll", {
    maxWidth: "100%",
    maxHeight: "320px",
    overflow: "auto",
    marginTop: "8px",
    border: "none",
    background: "rgb(var(--mdui-color-surface-container-low))",
    borderRadius: "var(--mdui-shape-corner-extra-small)"
});
globalStyle(".sc-logs-table", {
    width: "100%",
    minWidth: "28rem",
    borderCollapse: "collapse",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-table caption", {
    padding: "8px",
    textAlign: "start",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-logs-table th, .sc-logs-table td", {
    padding: "4px 8px",
    textAlign: "start",
    whiteSpace: "nowrap",
    borderTop: "none"
});
globalStyle(".sc-logs-table thead th", {
    position: "sticky",
    top: "0",
    background: "rgb(var(--mdui-color-surface-container-low))"
});
globalStyle(".sc-logs-table td", {
    textAlign: "end"
});
globalStyle(".sc-logs-error", {
    margin: "8px 0 0",
    color: "rgb(var(--mdui-color-error))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-note", {
    margin: "16px 0 0",
    textAlign: "center",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-empty", {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    gap: "8px",
    padding: "32px 16px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    textAlign: "center",
    border: "1px dashed var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-medium)",
    background: "transparent"
});
globalStyle(".sc-logs-empty p", {
    margin: "0"
});
globalStyle(".sc-logs-empty-hint", {
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-list", {
    listStyle: "none",
    margin: "0",
    padding: "0",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-medium)",
    background: "transparent",
    display: "flex",
    flexDirection: "column",
    gap: "0",
    overflow: "hidden"
});
globalStyle(".sc-logs-item", {
    background: "transparent",
    borderRadius: "0",
    overflow: "hidden"
});
globalStyle(".sc-logs-item + .sc-logs-item", {
    borderTop: "1px solid var(--sc-outline-variant)"
});
globalStyle(".sc-logs-row", {
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    gap: "16px",
    width: "100%",
    minWidth: "0",
    minHeight: "var(--sc-row-height)",
    boxSizing: "border-box",
    padding: "8px 16px",
    color: "rgb(var(--mdui-color-on-surface))",
    textAlign: "start"
});
globalStyle(".sc-logs-row-button", {
    background: "none",
    border: "0",
    font: "inherit",
    cursor: "pointer"
});
globalStyle(".sc-logs-level", {
    display: "inline-flex",
    alignItems: "center",
    flexShrink: "0",
    gap: "4px",
    minWidth: "88px",
    padding: "4px 8px",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-label-medium-size)"
});
globalStyle(".sc-logs-level-error, .sc-logs-level-failed", {
    background: "rgb(var(--mdui-color-error-container))",
    color: "rgb(var(--mdui-color-on-error-container))"
});
globalStyle(".sc-logs-level-warn", {
    background: "rgb(var(--mdui-color-tertiary-container))",
    color: "rgb(var(--mdui-color-on-tertiary-container))"
});
globalStyle(".sc-logs-level-info", {
    background: "rgb(var(--mdui-color-secondary-container))",
    color: "rgb(var(--mdui-color-on-secondary-container))"
});
globalStyle(".sc-logs-level-ok", {
    background: "rgb(var(--mdui-color-primary-container))",
    color: "rgb(var(--mdui-color-on-primary-container))"
});
globalStyle(".sc-logs-body", {
    display: "flex",
    flexDirection: "column",
    gap: "4px",
    minWidth: "0",
    flex: "1 0 12rem"
});
globalStyle(".sc-logs-msg", {
    fontSize: "var(--mdui-typescale-body-medium-size)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-logs-meta", {
    display: "flex",
    flexWrap: "wrap",
    gap: "8px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-logs-source", {
    paddingInline: "4px",
    border: "none",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    borderRadius: "var(--mdui-shape-corner-extra-small)"
});
globalStyle(".sc-logs-disclose", {
    display: "inline-flex",
    alignItems: "center",
    gap: "4px",
    flexShrink: "0",
    marginInlineStart: "auto",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-chevron", {
    display: "inline-flex",
    transition: "rotate 150ms ease-out"
});
globalStyle(".sc-logs-chevron-open", {
    rotate: "90deg"
});
globalStyle(".sc-logs-attrs", {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))",
    gap: "8px",
    margin: "0",
    padding: "16px",
    background: "rgb(var(--mdui-color-surface-container-low))"
});
globalStyle(".sc-logs-attrs div", {
    display: "flex",
    flexDirection: "column",
    gap: "4px",
    minWidth: "0"
});
globalStyle(".sc-logs-attrs dt", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-attrs dd", {
    margin: "0",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-logs-more", {
    display: "flex",
    justifyContent: "center",
    marginTop: "16px"
});
globalStyle(".sc-sr-only", {
    position: "absolute",
    width: "1px",
    height: "1px",
    padding: "0",
    margin: "-1px",
    overflow: "hidden",
    clip: "rect(0,0,0,0)",
    whiteSpace: "nowrap",
    border: "0"
});
globalStyle(".sc-logs-plot", {
    "@container": {
        "sc-logs (max-width: 599.98px)": {
            gap: "2px"
        }
    }
});
globalStyle(".sc-logs-source-button, .sc-logs-table-wrap summary", {
    "@container": {
        "sc-logs (max-width: 599.98px)": {
            minBlockSize: "44px"
        }
    }
});
globalStyle(".sc-logs-fields", {
    "@container": {
        "sc-logs (max-width: 599.98px)": {
            flexDirection: "column",
            alignItems: "stretch"
        }
    }
});
globalStyle(".sc-logs-fields > .sc-field", {
    "@container": {
        "sc-logs (max-width: 599.98px)": {
            flex: "none",
            width: "100%",
            maxWidth: "none"
        }
    }
});
globalStyle(".sc-logs-row", {
    "@container": {
        "sc-logs (max-width: 599.98px)": {
            alignItems: "flex-start",
            gap: "8px 12px",
            paddingInline: "12px"
        }
    }
});
globalStyle(".sc-logs-body", {
    "@container": {
        "sc-logs (max-width: 599.98px)": {
            flexBasis: "calc(100% - 100px)"
        }
    }
});
globalStyle(".sc-logs-disclose", {
    "@container": {
        "sc-logs (max-width: 599.98px)": {
            width: "100%",
            justifyContent: "flex-end"
        }
    }
});
globalStyle(".sc-logs-attrs", {
    "@container": {
        "sc-logs (max-width: 599.98px)": {
            gridTemplateColumns: "minmax(0, 1fr)"
        }
    }
});
globalStyle(".sc-logs-chevron", {
    "@media": {
        "(prefers-reduced-motion: reduce)": {
            transition: "none"
        }
    }
});
