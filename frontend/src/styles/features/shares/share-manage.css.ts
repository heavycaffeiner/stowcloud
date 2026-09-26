import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-share-dialog", {
    vars: { "--width": "min(42rem, calc(100vw - 32px))" }
});
globalStyle(".sc-share-dialog::part(panel)", {
    maxInlineSize: "calc(100vw - 32px)"
});
globalStyle(".sc-share-issued", {
    padding: "16px",
    marginBottom: "16px",
    borderRadius: "var(--mdui-shape-corner-medium, 12px)",
    background: "var(--mdui-color-secondary-container, var(--m3c-secondary-container))",
    color: "var(--mdui-color-on-secondary-container, var(--m3c-on-secondary-container))"
});
globalStyle(".sc-share-issued-note", {
    margin: "0 0 8px",
    overflowWrap: "anywhere"
});
globalStyle(".sc-share-url-row", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    minWidth: "0"
});
globalStyle(".sc-share-url", {
    display: "block",
    flex: "1 1 auto",
    minWidth: "0",
    maxWidth: "100%",
    boxSizing: "border-box",
    padding: "8px",
    border: "0",
    borderRadius: "var(--mdui-shape-corner-extra-small, 4px)",
    background: "var(--mdui-color-surface, var(--m3c-surface))",
    color: "var(--mdui-color-on-surface, var(--m3c-on-surface))",
    font: "inherit",
    overflowWrap: "anywhere",
    resize: "vertical",
    userSelect: "all",
    whiteSpace: "pre-wrap"
});
globalStyle(".sc-share-url:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-share-copy-feedback", {
    margin: "8px 0 0",
    fontSize: "var(--mdui-typescale-body-small-size, .875rem)"
});
globalStyle(".sc-share-copy-feedback--error,\n.sc-share-error", {
    color: "var(--mdui-color-error, var(--m3c-error))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-share-loading", {
    display: "flex",
    justifyContent: "center",
    padding: "24px"
});
globalStyle(".sc-share-empty", {
    color: "var(--mdui-color-on-surface-variant, var(--m3c-on-surface-variant))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-share-list", {
    display: "flex",
    flexDirection: "column",
    gap: "8px",
    minWidth: "0",
    margin: "0 0 16px",
    padding: "0",
    listStyle: "none"
});
globalStyle(".sc-share-item", {
    minWidth: "0",
    padding: "12px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-medium, 12px)",
    background: "rgb(var(--mdui-color-surface-container))",
    boxShadow: "0 2px 6px rgba(0, 0, 0, .08)",
    transition: "background-color 140ms ease"
});
globalStyle(".sc-share-item:hover", {
    background: "rgb(var(--mdui-color-surface-container-high))"
});
globalStyle(".sc-share-item-row", {
    display: "flex",
    alignItems: "flex-start",
    gap: "12px",
    minWidth: "0"
});
globalStyle(".sc-share-item-main", {
    display: "flex",
    flex: "1 1 auto",
    flexDirection: "column",
    minWidth: "0"
});
globalStyle(".sc-share-item-label", {
    color: "var(--mdui-color-on-surface, var(--m3c-on-surface))",
    fontWeight: "500",
    overflowWrap: "anywhere"
});
globalStyle(".sc-share-item-meta", {
    color: "var(--mdui-color-on-surface-variant, var(--m3c-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size, .875rem)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-share-item-actions", {
    display: "flex",
    flex: "0 0 auto",
    alignItems: "center",
    flexWrap: "wrap",
    justifyContent: "flex-end"
});
globalStyle(".sc-share-create-form,\n.sc-share-edit-form", {
    display: "flex",
    flexDirection: "column",
    gap: "12px",
    minWidth: "0",
    padding: "12px",
    borderRadius: "var(--mdui-shape-corner-medium, 12px)",
    background: "var(--mdui-color-surface-container-low, var(--m3c-surface-container-low))"
});
globalStyle(".sc-share-create-form h3", {
    margin: "0",
    fontSize: "var(--mdui-typescale-title-medium-size, 1rem)",
    fontWeight: "var(--mdui-typescale-title-medium-weight, 500)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height, 1.5rem)"
});
globalStyle(".sc-share-perm-row", {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "12px 16px"
});
globalStyle(".sc-share-hint", {
    margin: "0",
    color: "var(--mdui-color-on-surface-variant, var(--m3c-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size, .875rem)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-share-edit-actions", {
    display: "flex",
    alignItems: "center",
    justifyContent: "flex-end",
    flexWrap: "wrap",
    gap: "8px"
});
globalStyle(".sc-share-dialog", {
    "@media": {
        "(max-width: 599.98px)": {
            vars: { "--width": "calc(100vw - 16px)" }
        }
    }
});
globalStyle(".sc-share-dialog::part(panel)", {
    "@media": {
        "(max-width: 599.98px)": {
            maxInlineSize: "calc(100vw - 16px)"
        }
    }
});
globalStyle(".sc-share-issued,\n  .sc-share-item,\n  .sc-share-create-form,\n  .sc-share-edit-form", {
    "@media": {
        "(max-width: 599.98px)": {
            padding: "12px"
        }
    }
});
globalStyle(".sc-share-item-row", {
    "@media": {
        "(max-width: 599.98px)": {
            flexWrap: "wrap"
        }
    }
});
globalStyle(".sc-share-item-main", {
    "@media": {
        "(max-width: 599.98px)": {
            flexBasis: "calc(100% - 36px)"
        }
    }
});
globalStyle(".sc-share-item-actions", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "100%",
            justifyContent: "flex-end"
        }
    }
});
