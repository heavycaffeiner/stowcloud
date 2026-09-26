import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-restart-status", {
    display: "inline-flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "12px",
    maxWidth: "100%",
    overflowWrap: "anywhere"
});
globalStyle(".sc-restart-error", {
    color: "rgb(var(--mdui-color-error))",
    overflowWrap: "anywhere"
});
