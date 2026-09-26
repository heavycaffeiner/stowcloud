import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-admin-inner", {
    display: "grid",
    gap: "0",
    minWidth: "0"
});
globalStyle(".sc-admin-page-section", {
    minWidth: "0"
});
globalStyle(".sc-admin-page-section h2", {
    margin: "0 0 8px",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-title-large-size)",
    fontWeight: "var(--mdui-typescale-title-large-weight)",
    letterSpacing: "var(--mdui-typescale-title-large-tracking)",
    lineHeight: "var(--mdui-typescale-title-large-line-height)"
});
globalStyle(".sc-admin-loading", {
    display: "flex",
    alignItems: "center",
    gap: "12px",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-admin-denied", {
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-page-error", {
    margin: "0",
    color: "rgb(var(--mdui-color-error))",
    overflowWrap: "anywhere"
});
