import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-middle-ellipsis", {
    display: "flex",
    minWidth: "0",
    whiteSpace: "nowrap"
});
globalStyle(".sc-middle-ellipsis-start", {
    minWidth: "0",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-middle-ellipsis-end", {
    flex: "none",
    whiteSpace: "nowrap"
});
