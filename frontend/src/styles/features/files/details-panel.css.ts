import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-details", {
    boxSizing: "border-box",
    flex: "none",
    width: "340px",
    alignSelf: "stretch",
    padding: "12px 16px 24px",
    borderInlineStart: "1px solid var(--sc-outline-variant)",
    background: "var(--sc-container-surface)",
    color: "var(--sc-content-primary)",
    overflowY: "auto"
});
globalStyle(".sc-details--sheet", {
    position: "fixed",
    inset: "0",
    zIndex: "30",
    width: "auto",
    borderInlineStart: "0",
    background: "var(--sc-raised-surface)",
    paddingBottom: "calc(16px + env(safe-area-inset-bottom, 0px))"
});
globalStyle(".sc-details-head", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    minHeight: "48px",
    marginBottom: "12px"
});
globalStyle(".sc-details-head-icon", {
    display: "inline-flex",
    flex: "none"
});
globalStyle(".sc-details-title", {
    flex: "1",
    minWidth: "0",
    margin: "0",
    fontSize: "var(--mdui-typescale-title-medium-size)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height)",
    fontWeight: "600",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-details-head button", {
    minWidth: "var(--sc-control-min)",
    minHeight: "var(--sc-control-min)",
    border: "0",
    borderRadius: "50%",
    background: "transparent",
    color: "inherit",
    cursor: "pointer",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center"
});
globalStyle(".sc-details-head button:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-details-head button:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 8%, transparent)"
});
globalStyle(".sc-details-summary", {
    display: "flex",
    alignItems: "center",
    gap: "12px",
    minWidth: "0",
    padding: "8px 0 16px"
});
globalStyle(".sc-details-summary-icon", {
    display: "inline-flex",
    flex: "none"
});
globalStyle(".sc-details-summary-title", {
    fontSize: "var(--mdui-typescale-title-small-size)",
    lineHeight: "var(--mdui-typescale-title-small-line-height)",
    fontWeight: "600",
    overflowWrap: "anywhere"
});
globalStyle(".sc-details-summary-desc", {
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-details-actions", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    marginBottom: "18px"
});
globalStyle(".sc-details-actions .sc-button-wrap", {
    flex: "1"
});
globalStyle(".sc-details-primary-btn", {
    flex: "1",
    minHeight: "var(--sc-control-min)",
    borderRadius: "var(--sc-radius-full)",
    border: "none",
    background: "rgb(var(--mdui-color-primary))",
    color: "rgb(var(--mdui-color-on-primary))",
    fontFamily: "inherit",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    fontWeight: "600",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    gap: "8px",
    cursor: "pointer",
    transition: "background-color 140ms ease"
});
globalStyle(".sc-details-primary-btn:hover", {
    background: "color-mix(in srgb, rgb(var(--mdui-color-primary)) 88%, var(--sc-content-primary))"
});
globalStyle(".sc-details-primary-btn:active", {
    transform: "scale(0.98)"
});
globalStyle(".sc-details-circle-btn", {
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    borderRadius: "50%",
    border: "1px solid var(--sc-outline-variant)",
    background: "color-mix(in srgb, var(--sc-content-primary) 5%, transparent)",
    color: "var(--sc-content-secondary)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    cursor: "pointer",
    padding: "0",
    transition: "background-color 140ms ease, color 140ms ease"
});
globalStyle(".sc-details-circle-btn:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 10%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-details-section-heading", {
    fontSize: "var(--mdui-typescale-title-small-size)",
    lineHeight: "var(--mdui-typescale-title-small-line-height)",
    fontWeight: "600",
    color: "var(--sc-content-secondary)",
    margin: "12px 0 8px"
});
globalStyle(".sc-details-warning", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    margin: "0 0 12px",
    padding: "8px 12px",
    borderRadius: "var(--sc-radius-small)",
    background: "var(--sc-state-error)",
    color: "var(--sc-state-error-content)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-details-fields", {
    display: "flex",
    flexDirection: "column",
    gap: "12px",
    margin: "0",
    padding: "0"
});
globalStyle(".sc-details-fields > div", {
    display: "flex",
    flexDirection: "column",
    gap: "2px"
});
globalStyle(".sc-details-fields dt", {
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-label-medium-size)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height)",
    fontWeight: "500",
    margin: "0"
});
globalStyle(".sc-details-fields dd", {
    color: "var(--sc-content-primary)",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height)",
    fontWeight: "400",
    margin: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-details-fields small", {
    display: "block",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    marginTop: "2px"
});
