import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-picker", {
    display: "flex",
    flexDirection: "column",
    gap: "8px",
    minInlineSize: "0",
    width: "min(420px, 80vw)",
    maxWidth: "100%"
});
globalStyle(".sc-picker-nav", {
    display: "flex",
    alignItems: "center",
    gap: "8px"
});
globalStyle(".sc-picker-here", {
    margin: "0",
    overflowWrap: "anywhere",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-picker-body", {
    minHeight: "0",
    minWidth: "0",
    maxHeight: "40vh",
    overflowY: "auto",
    border: "none",
    background: "rgb(var(--mdui-color-surface-container-high))",
    borderRadius: "8px"
});
globalStyle(".sc-picker-status", {
    margin: "0",
    padding: "16px",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-picker-entries", {
    listStyle: "none",
    margin: "0",
    padding: "4px"
});
globalStyle(".sc-picker-entry", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    width: "100%",
    minHeight: "40px",
    padding: "8px",
    border: "none",
    borderRadius: "4px",
    background: "none",
    color: "inherit",
    textAlign: "left"
});
globalStyle(".sc-picker-entry > span:last-child", {
    minWidth: "0",
    overflowWrap: "anywhere"
});
globalStyle("button.sc-picker-entry", {
    cursor: "pointer"
});
globalStyle("button.sc-picker-entry:hover", {
    background: "var(--sc-raised-surface)"
});
globalStyle(".sc-picker-entry--selected", {
    background: "var(--sc-state-selection)",
    color: "var(--sc-state-selection-content)"
});
globalStyle(".sc-picker-entry--disabled", {
    color: "var(--sc-content-secondary)",
    opacity: "0.6"
});
