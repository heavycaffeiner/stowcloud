import { globalStyle } from '@vanilla-extract/css';
import "./file-table.css.ts";
import "./file-grid.css.ts";
import "./file-tree.css.ts";
import "./details-panel.css.ts";
globalStyle(".sc-browse-dialog", {
    vars: { "--mdui-color-surface": "var(--mdui-color-surface-container-high)" }
});
globalStyle(".sc-browse-dialog-body", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    paddingBlock: "12px 4px",
    minWidth: "min(360px, 80vw)"
});
globalStyle(".sc-delete-dialog-external-warning", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    color: "rgb(var(--mdui-color-tertiary))"
});
globalStyle(".sc-dest", {
    minWidth: "min(360px, 72vw)"
});
globalStyle(".sc-dest-prompt", {
    margin: "0 0 8px",
    fontSize: "var(--mdui-typescale-body-medium-size, .875rem)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height, 1.25rem)"
});
globalStyle(".sc-dest-tree", {
    maxHeight: "40vh",
    overflowY: "auto",
    padding: "4px",
    border: "none",
    background: "rgb(var(--mdui-color-surface-container-high))",
    borderRadius: "var(--mdui-shape-corner-small, 8px)"
});
globalStyle(".sc-dest-tree ul", {
    listStyle: "none",
    margin: "0",
    padding: "0"
});
globalStyle(".sc-dest-status", {
    minHeight: "20px",
    margin: "8px 0 0",
    color: "var(--sc-content-secondary)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-dest-status--warn", {
    minHeight: "20px",
    margin: "8px 0 0",
    color: "rgb(var(--mdui-color-error))",
    overflowWrap: "anywhere"
});
