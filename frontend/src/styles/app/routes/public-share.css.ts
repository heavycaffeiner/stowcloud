import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-public-share", {
    width: "min(100%, 40rem)",
    minWidth: "0",
    overflowX: "hidden",
    minHeight: "100dvh",
    marginInline: "auto",
    padding: "var(--sc-page-pad)"
});
globalStyle(".sc-public-share-header", {
    display: "flex",
    flexWrap: "wrap",
    gap: "4px 8px",
    marginBottom: "32px",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    fontWeight: "var(--mdui-typescale-body-medium-weight)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height)",
    letterSpacing: "var(--mdui-typescale-body-medium-tracking)"
});
globalStyle(".sc-public-share h1", {
    fontSize: "var(--mdui-typescale-headline-small-size)",
    fontWeight: "var(--mdui-typescale-headline-small-weight)",
    lineHeight: "var(--mdui-typescale-headline-small-line-height)",
    letterSpacing: "var(--mdui-typescale-headline-small-tracking)"
});
globalStyle(".sc-public-share-title", {
    margin: "0 0 16px",
    overflowWrap: "anywhere"
});
globalStyle(".sc-public-share-status", {
    margin: "0 0 16px",
    overflowWrap: "anywhere",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-public-share-status-error", {
    color: "var(--m3c-error)"
});
globalStyle(".sc-public-share-state,\n.sc-public-share-unlock", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    minWidth: "0"
});
globalStyle(".sc-public-share-state-error", {
    padding: "16px",
    borderRadius: "var(--sc-radius-medium)",
    background: "var(--sc-state-error)",
    color: "var(--sc-state-error-content)"
});
globalStyle(".sc-public-share-state p,\n.sc-public-share-unlock > h1", {
    margin: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-public-share-unlock", {
    maxWidth: "20rem",
    width: "100%"
});
globalStyle(".sc-public-share-unlock-actions", {
    alignSelf: "flex-start"
});
globalStyle(".sc-public-share-drop", {
    display: "flex",
    flexDirection: "column",
    alignItems: "flex-start",
    gap: "8px",
    marginBottom: "24px"
});
globalStyle(".sc-public-share-file", {
    display: "none"
});
globalStyle(".sc-public-share-list", {
    display: "flex",
    flexDirection: "column",
    gap: "0",
    minWidth: "0",
    margin: "0 0 24px",
    padding: "0",
    overflow: "hidden",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-medium)",
    background: "transparent",
    listStyle: "none"
});
globalStyle(".sc-public-share-row", {
    display: "flex",
    alignItems: "center",
    gap: "12px",
    minWidth: "0",
    minHeight: "52px",
    padding: "8px 16px",
    borderRadius: "0",
    background: "transparent",
    borderBottom: "1px solid var(--sc-outline-variant)",
    transition: "background-color 140ms ease"
});
globalStyle(".sc-public-share-row:hover", {
    background: "rgb(var(--mdui-color-surface-container-low))"
});
globalStyle(".sc-public-share-row:last-child", {
    borderBottom: "none"
});
globalStyle(".sc-public-share-row-empty", {
    justifyContent: "center",
    color: "var(--sc-content-secondary)",
    textAlign: "center"
});
globalStyle(".sc-public-share-icon", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    flex: "none",
    color: "var(--sc-icon-color)"
});
globalStyle(".sc-public-share-name", {
    flex: "1",
    minWidth: "0",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    color: "var(--sc-content-primary)",
    textAlign: "start"
});
globalStyle("button.sc-public-share-name", {
    border: "none",
    background: "transparent",
    cursor: "pointer",
    padding: "0"
});
globalStyle(".sc-public-share-size", {
    flex: "none",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    whiteSpace: "nowrap",
    fontVariantNumeric: "tabular-nums"
});
globalStyle(".sc-public-share-action", {
    flex: "none"
});
globalStyle(".sc-public-share-crumbs", {
    marginBottom: "16px",
    minWidth: "0"
});
globalStyle(".sc-public-share-crumbs ol", {
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    gap: "4px",
    minWidth: "0",
    maxWidth: "100%",
    margin: "0",
    padding: "0",
    listStyle: "none"
});
globalStyle(".sc-public-share-crumbs li", {
    display: "flex",
    alignItems: "center",
    gap: "4px",
    minWidth: "0",
    maxWidth: "100%"
});
globalStyle(".sc-public-share-crumb,\n.sc-public-share-folder", {
    minWidth: "0",
    minHeight: "40px",
    maxWidth: "100%",
    padding: "4px 8px",
    overflow: "hidden",
    border: "none",
    borderRadius: "var(--sc-radius-small)",
    background: "none",
    color: "rgb(var(--mdui-color-primary))",
    font: "inherit",
    textAlign: "start",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    cursor: "pointer"
});
globalStyle(".sc-public-share-crumb:hover,\n.sc-public-share-folder:hover", {
    background: "var(--sc-raised-surface)"
});
globalStyle(".sc-public-share-crumb-sep", {
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-public-share-crumbs [aria-current='page']", {
    minWidth: "0",
    maxWidth: "100%",
    overflowWrap: "anywhere",
    fontWeight: "var(--mdui-typescale-label-large-weight)"
});
globalStyle(".sc-public-share-state > .sc-button-wrap", {
    alignSelf: "flex-start"
});
globalStyle(".sc-public-share", {
    "@media": {
        "(max-width: 599.98px)": {
            paddingBlock: "16px"
        }
    }
});
globalStyle(".sc-public-share-header", {
    "@media": {
        "(max-width: 599.98px)": {
            marginBottom: "24px"
        }
    }
});
globalStyle(".sc-public-share-crumb,\n  .sc-public-share-folder,\n  .sc-public-share .sc-button-wrap > mdui-button", {
    "@media": {
        "(max-width: 599.98px)": {
            minHeight: "44px"
        }
    }
});
globalStyle(".sc-public-share-row", {
    "@media": {
        "(max-width: 599.98px)": {
            gridTemplateColumns: "minmax(0, 1fr) auto",
            paddingInline: "8px"
        }
    }
});
globalStyle(".sc-public-share-row-actions,\n  .sc-public-share-row > .sc-button-wrap", {
    "@media": {
        "(max-width: 599.98px)": {
            gridColumn: "1 / -1",
            justifySelf: "end"
        }
    }
});
globalStyle(".sc-public-share-size", {
    "@media": {
        "(max-width: 599.98px)": {
            maxWidth: "min(42vw, 12rem)"
        }
    }
});
globalStyle(".sc-public-share-row-actions .sc-button-wrap", {
    "@media": {
        "(max-width: 599.98px)": {
            maxWidth: "100%"
        }
    }
});
