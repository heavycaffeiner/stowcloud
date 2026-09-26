import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-admin-section", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    minWidth: "0",
    animation: "sc-fade-in-up 220ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-admin-section-header", {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: "24px",
    minWidth: "0"
});
globalStyle(".sc-admin-section-hint,\n.sc-admin-section-field-hint", {
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-admin-section-hint", {
    maxWidth: "40rem",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-section-error", {
    margin: "0",
    color: "rgb(var(--mdui-color-error))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-list", {
    listStyle: "none",
    margin: "0",
    padding: "0",
    overflow: "hidden",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-medium)",
    background: "transparent",
    display: "flex",
    flexDirection: "column",
    gap: "0"
});
globalStyle(".sc-admin-list > li", {
    minWidth: "0"
});
globalStyle(".sc-admin-list > li + li", {
    borderTop: "1px solid var(--sc-outline-variant)"
});
globalStyle(".sc-admin-row", {
    display: "flex",
    alignItems: "center",
    gap: "12px",
    minWidth: "0",
    minBlockSize: "56px",
    padding: "8px 16px",
    borderRadius: "0",
    background: "transparent",
    transition: "background-color 140ms ease"
});
globalStyle(".sc-admin-row:hover", {
    background: "rgb(var(--mdui-color-surface-container-low))"
});
globalStyle(".sc-admin-row-body", {
    minWidth: "0",
    flex: "1 1 12rem"
});
globalStyle(".sc-admin-row-title", {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "8px",
    minWidth: "0",
    fontSize: "var(--mdui-typescale-body-large-size)",
    fontWeight: "var(--mdui-typescale-body-large-weight)",
    lineHeight: "var(--mdui-typescale-body-large-line-height)"
});
globalStyle(".sc-admin-row-name", {
    minWidth: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-row-supporting", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-row-actions", {
    display: "flex",
    alignItems: "center",
    justifyContent: "flex-end",
    flexWrap: "wrap",
    gap: "8px",
    flex: "0 0 auto"
});
globalStyle(".sc-user-mgmt > .sc-admin-list > li > .sc-admin-row > .sc-admin-chip", {
    flex: "0 0 10rem",
    width: "10rem",
    maxWidth: "100%",
    height: "auto",
    minHeight: "28px",
    whiteSpace: "normal",
    overflowWrap: "anywhere",
    fontVariantNumeric: "tabular-nums"
});
globalStyle(".sc-user-mgmt > .sc-admin-list > li > .sc-admin-row > button.sc-admin-chip", {
    minHeight: "var(--sc-control-min)"
});
globalStyle(".sc-admin-state", {
    display: "flex",
    alignItems: "center",
    gap: "6px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-label-small-size)",
    lineHeight: "var(--mdui-typescale-label-small-line-height)"
});
globalStyle(".sc-admin-chip", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    minBlockSize: "24px",
    padding: "2px 9px",
    borderRadius: "var(--mdui-shape-corner-full)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-label-small-size)",
    fontWeight: "var(--mdui-typescale-label-small-weight)",
    lineHeight: "var(--mdui-typescale-label-small-line-height)",
    whiteSpace: "nowrap"
});
globalStyle("button.sc-admin-chip", {
    minHeight: "var(--sc-control-min)",
    border: "1px solid var(--sc-outline-variant)",
    font: "inherit",
    cursor: "pointer",
    paddingInline: "12px",
    transition: "background-color 140ms ease, border-color 140ms ease, opacity 140ms ease, transform 120ms ease"
});
globalStyle("button.sc-admin-chip:hover", {
    borderColor: "var(--sc-outline)",
    filter: "brightness(0.92)"
});
globalStyle("button.sc-admin-chip:active", {
    transform: "scale(0.95)"
});
globalStyle("button.sc-admin-chip:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-admin-chip:has(.sc-button-wrap)", {
    minHeight: "44px",
    gap: "4px",
    padding: "0 0 0 12px"
});
globalStyle(".sc-admin-chip .sc-button-wrap", {
    margin: "0",
    display: "inline-flex",
    alignItems: "center"
});
globalStyle(".sc-admin-chip mdui-button.sc-button-square", {
    inlineSize: "44px",
    minInlineSize: "44px",
    blockSize: "44px",
    minBlockSize: "44px",
    borderRadius: "var(--mdui-shape-corner-full)"
});
globalStyle(".sc-admin-chip mdui-button.sc-button-square svg", {
    width: "14px",
    height: "14px"
});
globalStyle(".sc-admin-chip-muted", {
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-admin-chip-danger", {
    background: "rgb(var(--mdui-color-error-container))",
    color: "rgb(var(--mdui-color-on-error-container))"
});
globalStyle(".sc-admin-form", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    width: "min(36rem, 100%)",
    minWidth: "0"
});
globalStyle(".sc-admin-form-row", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    minWidth: "0"
});
globalStyle(".sc-admin-form-row > select", {
    flex: "1 1 auto",
    minWidth: "0"
});
globalStyle(".sc-admin-select", {
    minBlockSize: "3rem",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    padding: "0 16px",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface))",
    font: "inherit"
});
globalStyle(".sc-admin-dialog-actions", {
    display: "flex",
    alignItems: "center",
    justifyContent: "flex-end",
    flexWrap: "wrap",
    gap: "8px"
});
globalStyle(".sc-admin-empty", {
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
globalStyle(".sc-admin-empty p", {
    margin: "0"
});
globalStyle(".sc-admin-dialog-warning", {
    display: "flex",
    alignItems: "flex-start",
    gap: "8px",
    margin: "0 0 16px",
    padding: "12px",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-error-container))",
    color: "rgb(var(--mdui-color-on-error-container))"
});
globalStyle(".sc-admin-permgrid", {
    display: "flex",
    flexDirection: "column",
    gap: "0",
    maxWidth: "100%",
    overflow: "hidden",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-small)",
    background: "transparent"
});
globalStyle(".sc-admin-permgrid-head,\n.sc-admin-permgrid-row", {
    display: "grid",
    gridTemplateColumns: "1fr 72px 72px",
    alignItems: "center",
    gap: "8px",
    padding: "8px 12px"
});
globalStyle(".sc-admin-permgrid-head", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    background: "rgb(var(--mdui-color-surface-container-low))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-admin-permgrid-row", {
    background: "transparent"
});
globalStyle(".sc-admin-permgrid-row + .sc-admin-permgrid-row", {
    borderTop: "1px solid var(--sc-outline-variant)"
});
globalStyle(".sc-admin-permgrid-cell", {
    display: "flex",
    justifyContent: "center"
});
globalStyle(".sc-admin-grant-supporting", {
    display: "flex",
    flexDirection: "column",
    gap: "4px",
    minWidth: "0",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-grant-summary-deny", {
    color: "rgb(var(--mdui-color-error))"
});
globalStyle(".sc-admin-grant-warning", {
    display: "flex",
    alignItems: "flex-start",
    gap: "4px",
    color: "rgb(var(--mdui-color-error))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-grant-perms", {
    display: "flex",
    flexWrap: "wrap",
    gap: "4px"
});
globalStyle(".sc-admin-grant-chevron", {
    display: "inline-flex",
    transition: "transform 150ms ease"
});
globalStyle(".sc-admin-grant-chevron-open", {
    transform: "rotate(90deg)"
});
globalStyle(".sc-admin-facts, .sc-user-oidc-facts", {
    display: "flex",
    flexDirection: "column",
    gap: "8px",
    margin: "0 0 16px"
});
globalStyle(".sc-admin-facts dt, .sc-user-oidc-facts dt", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)"
});
globalStyle(".sc-admin-facts dd, .sc-user-oidc-facts dd", {
    margin: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-chips", {
    display: "flex",
    flexWrap: "wrap",
    gap: "8px",
    listStyle: "none",
    margin: "0",
    padding: "0"
});
globalStyle(".sc-user-oidc-hint", {
    margin: "0 0 8px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "1.45"
});
globalStyle(".sc-user-oidc-error", {
    margin: "0",
    color: "rgb(var(--mdui-color-error))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "1.45"
});
globalStyle(".sc-user-oidc-warning", {
    display: "flex",
    alignItems: "flex-start",
    gap: "8px",
    margin: "0 0 16px",
    padding: "12px",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-error-container))",
    color: "rgb(var(--mdui-color-on-error-container))"
});
globalStyle(".sc-admin-section-header", {
    "@media": {
        "(max-width: 599.98px)": {
            flexDirection: "column",
            alignItems: "stretch",
            gap: "12px"
        }
    }
});
globalStyle(".sc-admin-section-header > .sc-button-wrap", {
    "@media": {
        "(max-width: 599.98px)": {
            alignSelf: "flex-start"
        }
    }
});
globalStyle(".sc-admin-row", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "flex-start",
            flexWrap: "wrap",
            paddingInline: "12px"
        }
    }
});
globalStyle(".sc-admin-row-body", {
    "@media": {
        "(max-width: 599.98px)": {
            flexBasis: "100%"
        }
    }
});
globalStyle(".sc-admin-row-actions", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "100%",
            justifyContent: "flex-start"
        }
    }
});
globalStyle("button.sc-admin-chip", {
    "@media": {
        "(max-width: 599.98px)": {
            minBlockSize: "44px"
        }
    }
});
globalStyle(".sc-admin-form-row", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "stretch",
            flexDirection: "column"
        }
    }
});
globalStyle(".sc-admin-permgrid-head,\n  .sc-admin-permgrid-row", {
    "@media": {
        "(max-width: 599.98px)": {
            gridTemplateColumns: "minmax(0, 1fr) 52px 52px",
            paddingInline: "8px"
        }
    }
});
