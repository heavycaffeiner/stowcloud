import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-page", {
    width: "min(100%, 72rem)",
    marginInline: "auto",
    padding: "var(--sc-page-pad)"
});
globalStyle(".sc-page-header", {
    display: "flex",
    alignItems: "center",
    gap: "12px",
    marginBottom: "8px"
});
globalStyle(".sc-page-header h1", {
    flex: "1"
});
globalStyle(".sc-page-hint", {
    margin: "0 0 16px",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-page-error,\n.sc-secondary-page-error", {
    color: "var(--m3c-error)"
});
globalStyle(".sc-secondary-page-error", {
    margin: "0 0 16px",
    padding: "12px 16px",
    overflowWrap: "anywhere",
    borderRadius: "8px",
    background: "var(--sc-state-error)",
    color: "var(--sc-state-error-content)"
});
globalStyle(".sc-page-row-meta", {
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-page-actions", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    margin: "0 0 16px"
});
globalStyle(".sc-search-page", {
    display: "flex",
    minHeight: "0",
    height: "100%",
    minWidth: "0",
    overflow: "hidden",
    padding: "var(--sc-page-pad)"
});
globalStyle(".sc-search-page-inner", {
    display: "flex",
    flexDirection: "column",
    gap: "12px",
    width: "100%",
    minWidth: "0",
    minHeight: "0"
});
globalStyle(".sc-search-page-header", {
    display: "flex",
    alignItems: "center",
    gap: "8px"
});
globalStyle(".sc-search-page-header h1", {
    margin: "0",
    fontSize: "var(--mdui-typescale-headline-small-size)",
    fontWeight: "var(--mdui-typescale-headline-small-weight)",
    lineHeight: "var(--mdui-typescale-headline-small-line-height)",
    letterSpacing: "var(--mdui-typescale-headline-small-tracking)"
});
globalStyle(".sc-secondary-page", {
    height: "100%",
    minWidth: "0",
    overflowX: "hidden",
    overflowY: "auto",
    wordBreak: "keep-all"
});
globalStyle(".sc-secondary-page-inner", {
    display: "flex",
    flexDirection: "column",
    width: "min(100%, 860px)",
    minWidth: "0",
    minHeight: "100%",
    marginInline: "auto",
    padding: "var(--sc-page-pad)"
});
globalStyle(".sc-secondary-page-header", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    marginBottom: "16px",
    flex: "none",
    minWidth: "0"
});
globalStyle(".sc-secondary-page-header h1", {
    flex: "1",
    minWidth: "0",
    margin: "0",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-headline-small-size)",
    fontWeight: "var(--mdui-typescale-headline-small-weight)",
    lineHeight: "var(--mdui-typescale-headline-small-line-height)",
    letterSpacing: "var(--mdui-typescale-headline-small-tracking)"
});
globalStyle(".sc-secondary-page-coverage", {
    margin: "-8px 0 16px",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-secondary-page-list", {
    display: "flex",
    flexDirection: "column",
    margin: "0",
    padding: "0",
    listStyle: "none",
    minWidth: "0"
});
globalStyle(".sc-secondary-page-row", {
    display: "flex",
    alignItems: "center",
    gap: "12px",
    width: "100%",
    minHeight: "56px",
    padding: "8px 16px",
    border: "0",
    borderBottom: "1px solid var(--m3c-outline-variant)",
    background: "none",
    color: "inherit",
    font: "inherit",
    textAlign: "start",
    cursor: "pointer"
});
globalStyle(".sc-secondary-page-row:hover", {
    background: "var(--sc-container-surface)"
});
globalStyle(".sc-secondary-page-row:focus-visible,\n.sc-route-back:focus-visible,\n.sc-route-icon-button:focus-visible,\n.sc-search-filter-button:focus-visible,\n.sc-search-filter-menu-trigger:focus-visible,\n.sc-search-chip:focus-visible,\n.sc-search-sort-trigger:focus-visible,\n.sc-trash-notice button:focus-visible,\n.sc-trash-operation-heading button:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-secondary-page-icon", {
    display: "inline-flex",
    flex: "none",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-secondary-page-text", {
    display: "flex",
    flex: "1",
    flexDirection: "column",
    minWidth: "0"
});
globalStyle(".sc-secondary-page-name,\n.sc-secondary-page-path", {
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-secondary-page-path,\n.sc-secondary-page-meta", {
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking)",
    whiteSpace: "nowrap"
});
globalStyle(".sc-secondary-page-meta", {
    flexShrink: "0"
});
globalStyle(".sc-secondary-page-loading,\n.sc-secondary-page-empty", {
    display: "grid",
    flex: "1 1 12rem",
    placeItems: "center",
    minHeight: "8rem",
    margin: "0",
    padding: "32px",
    color: "var(--sc-content-secondary)",
    textAlign: "center"
});
globalStyle(".sc-route-back,\n.sc-route-icon-button", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    width: "40px",
    height: "40px",
    padding: "0",
    border: "0",
    borderRadius: "50%",
    background: "transparent",
    color: "inherit",
    cursor: "pointer",
    flex: "none"
});
globalStyle(".sc-route-icon-button--danger", {
    color: "var(--m3c-error)"
});
globalStyle(".sc-route-icon-button:disabled", {
    cursor: "default",
    opacity: "0.38"
});
globalStyle(".sc-links-list > li,\n.sc-links-row", {
    minWidth: "0"
});
globalStyle(".sc-links-meta", {
    overflowWrap: "anywhere",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking)"
});
globalStyle(".sc-links-flag", {
    flex: "none",
    maxWidth: "100%",
    padding: "4px 8px",
    borderRadius: "var(--mdui-shape-corner-full, 999px)",
    background: "var(--sc-state-warning)",
    color: "var(--sc-state-warning-content)",
    fontSize: "var(--mdui-typescale-label-medium-size)",
    fontWeight: "var(--mdui-typescale-label-medium-weight)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height)",
    letterSpacing: "var(--mdui-typescale-label-medium-tracking)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-links-row--readonly", {
    cursor: "default",
    opacity: "0.72"
});
globalStyle(".sc-links-row > mdui-circular-progress", {
    flex: "none",
    width: "24px",
    height: "24px"
});
globalStyle(".sc-links-target-error", {
    margin: "4px 16px 12px",
    padding: "8px 12px",
    overflowWrap: "anywhere",
    borderRadius: "8px",
    background: "var(--sc-state-error)",
    color: "var(--sc-state-error-content)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking)"
});
globalStyle(".sc-trash-toolbar", {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    flexWrap: "wrap",
    gap: "8px",
    padding: "8px 16px",
    marginBottom: "8px",
    borderRadius: "12px",
    background: "var(--sc-container-surface)"
});
globalStyle(".sc-trash-select-all,\n.sc-trash-checkbox", {
    display: "inline-flex",
    alignItems: "center",
    gap: "8px",
    minHeight: "40px"
});
globalStyle(".sc-trash-checkbox", {
    justifyContent: "center",
    minWidth: "40px"
});
globalStyle(".sc-trash-toolbar-actions,\n.sc-trash-row-actions", {
    display: "flex",
    flex: "none",
    alignItems: "center",
    gap: "4px"
});
globalStyle(".sc-trash-row", {
    display: "flex",
    alignItems: "center",
    gap: "12px",
    padding: "8px 16px",
    minWidth: "0",
    borderBottom: "1px solid var(--m3c-outline-variant)"
});
globalStyle(".sc-trash-name", {
    flex: "1",
    minWidth: "0",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-trash-meta", {
    flexShrink: "0",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking)",
    whiteSpace: "nowrap"
});
globalStyle(".sc-trash-notice", {
    display: "grid",
    gridTemplateColumns: "minmax(0, 1fr) auto",
    alignItems: "center",
    gap: "8px",
    margin: "0 0 8px",
    padding: "8px 12px 8px 16px",
    overflowWrap: "anywhere",
    borderRadius: "8px",
    background: "var(--sc-state-warning)",
    color: "var(--sc-state-warning-content)"
});
globalStyle(".sc-trash-notice button,\n.sc-trash-operation-heading button", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    minWidth: "40px",
    minHeight: "40px",
    padding: "4px 8px",
    border: "0",
    borderRadius: "20px",
    background: "transparent",
    color: "inherit",
    cursor: "pointer"
});
globalStyle(".sc-trash-operation", {
    display: "grid",
    gap: "8px",
    margin: "12px 0",
    padding: "12px 16px",
    border: "none",
    borderRadius: "12px",
    background: "rgb(var(--mdui-color-surface-container))",
    boxShadow: "0 2px 8px rgba(0, 0, 0, 0.12)"
});
globalStyle(".sc-trash-operation-heading", {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: "8px",
    minWidth: "0"
});
globalStyle(".sc-trash-operation-heading h2", {
    minWidth: "0",
    margin: "0",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-title-medium-size)",
    fontWeight: "var(--mdui-typescale-title-medium-weight)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height)",
    letterSpacing: "var(--mdui-typescale-title-medium-tracking)"
});
globalStyle(".sc-trash-operation ul", {
    display: "grid",
    gap: "4px",
    margin: "0",
    padding: "0",
    listStyle: "none"
});
globalStyle(".sc-trash-operation li", {
    display: "flex",
    justifyContent: "space-between",
    gap: "12px",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking)"
});
globalStyle(".sc-trash-operation li > :first-child", {
    minWidth: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-trash-operation li > :last-child", {
    flex: "none"
});
globalStyle(".sc-trash-operation li.error", {
    color: "var(--m3c-error)"
});
globalStyle(".sc-page-row-meta,\n  .sc-recent .sc-secondary-page-meta", {
    "@media": {
        "(max-width: 599.98px)": {
            display: "none"
        }
    }
});
globalStyle(".sc-secondary-page-header", {
    "@media": {
        "(max-width: 599.98px)": {
            marginBottom: "12px"
        }
    }
});
globalStyle(".sc-secondary-page-row,\n  .sc-trash-row", {
    "@media": {
        "(max-width: 599.98px)": {
            gap: "8px",
            paddingInline: "8px"
        }
    }
});
globalStyle(".sc-route-back,\n  .sc-route-icon-button", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "44px",
            height: "44px"
        }
    }
});
globalStyle(".sc-secondary-page-empty,\n  .sc-secondary-page-loading", {
    "@media": {
        "(max-width: 599.98px)": {
            padding: "24px 16px"
        }
    }
});
globalStyle(".sc-links-row", {
    "@media": {
        "(max-width: 599.98px)": {
            display: "grid",
            gridTemplateColumns: "auto minmax(0, 1fr)"
        }
    }
});
globalStyle(".sc-links-flag", {
    "@media": {
        "(max-width: 599.98px)": {
            gridColumn: "2",
            justifySelf: "start",
            maxWidth: "100%"
        }
    }
});
globalStyle(".sc-trash-notice button,\n  .sc-trash-operation-heading button", {
    "@media": {
        "(max-width: 599.98px)": {
            minHeight: "44px"
        }
    }
});
globalStyle(".sc-trash-toolbar", {
    "@media": {
        "(max-width: 599.98px)": {
            paddingInline: "12px"
        }
    }
});
globalStyle(".sc-trash-toolbar-actions", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "100%",
            justifyContent: "flex-end"
        }
    }
});
globalStyle(".sc-secondary-page-meta,\n  .sc-trash-meta", {
    "@media": {
        "(max-width: 480px)": {
            display: "none"
        }
    }
});
