import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-admin-card", {
    scrollMarginTop: "24px",
    marginBottom: "0"
});
globalStyle(".sc-admin-card-icon", {
    display: "flex",
    flex: "none",
    alignItems: "center",
    justifyContent: "center",
    inlineSize: "40px",
    blockSize: "40px",
    borderRadius: "var(--mdui-shape-corner-small, 8px)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-admin-card-meta", {
    flex: "1 1 auto",
    minWidth: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-card-subtitle", {
    margin: "4px 0 0",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-section", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    minWidth: "0",
    marginBlock: "0"
});
globalStyle(".sc-admin-section > h2, .sc-admin-section > h3", {
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-title-large-size)",
    fontWeight: "var(--mdui-typescale-title-large-weight)",
    letterSpacing: "var(--mdui-typescale-title-large-tracking)",
    lineHeight: "var(--mdui-typescale-title-large-line-height)"
});
globalStyle(".sc-admin-section > h4", {
    margin: "8px 0 0",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-title-small-size)",
    lineHeight: "var(--mdui-typescale-title-small-line-height)"
});
globalStyle(".sc-admin-hint, .sc-admin-note", {
    maxWidth: "40rem",
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-error", {
    margin: "0",
    color: "rgb(var(--mdui-color-error))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-warning", {
    margin: "0",
    padding: "12px 16px",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-error-container))",
    color: "rgb(var(--mdui-color-on-error-container))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-saved", {
    margin: "0",
    color: "rgb(var(--mdui-color-primary))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-row", {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "12px",
    minWidth: "0"
});
globalStyle(".sc-admin-form", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    width: "min(36rem, 100%)",
    minWidth: "0"
});
globalStyle(".sc-path-row", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    minWidth: "0"
});
globalStyle(".sc-path-row > .sc-field", {
    flex: "1 1 auto",
    minWidth: "0"
});
globalStyle(".sc-shares-empty", {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    gap: "8px",
    padding: "32px 16px",
    textAlign: "center",
    border: "1px dashed var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-medium)",
    background: "transparent",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-shares-empty p", {
    margin: "0"
});
globalStyle(".sc-shares-list, .sc-storage-list", {
    listStyle: "none",
    margin: "0",
    padding: "0",
    minWidth: "0",
    overflow: "hidden",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-medium)",
    background: "transparent",
    display: "flex",
    flexDirection: "column",
    gap: "0"
});
globalStyle(".sc-shares-list li, .sc-storage-list li", {
    minWidth: "0",
    background: "transparent",
    borderRadius: "0",
    padding: "12px 16px",
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: "16px",
    transition: "background-color 140ms ease"
});
globalStyle(".sc-shares-list li", {
    padding: "0"
});
globalStyle(".sc-shares-list .sc-list-item", {
    flex: "1",
    minWidth: "0"
});
globalStyle(".sc-shares-list li:hover, .sc-storage-list li:hover", {
    background: "rgb(var(--mdui-color-surface-container-low))"
});
globalStyle(".sc-shares-list li + li, .sc-storage-list li + li", {
    borderTop: "1px solid var(--sc-outline-variant)"
});
globalStyle(".sc-storage-list strong", {
    flexShrink: "0",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontWeight: "500",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    textAlign: "end",
    overflowWrap: "anywhere"
});
globalStyle(".sc-storage-item-label", {
    display: "flex",
    alignItems: "center",
    gap: "10px",
    minWidth: "0",
    fontWeight: "500",
    color: "rgb(var(--mdui-color-on-surface))"
});
globalStyle(".sc-storage-item-label .sc-filename", {
    minWidth: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-storage-status-row", {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "12px"
});
globalStyle(".sc-storage-status-badge", {
    display: "inline-flex",
    alignItems: "center",
    gap: "6px",
    minHeight: "28px",
    padding: "2px 10px",
    borderRadius: "var(--mdui-shape-corner-full)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-label-medium-size)",
    fontWeight: "500"
});
globalStyle(".sc-storage-status-badge-on", {
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-storage-toggle-row", {
    display: "flex",
    alignItems: "center",
    gap: "12px"
});
globalStyle(".sc-shares-enc,\n.sc-shares-enc-note,\n.sc-shares-enc-salt-row", {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "8px",
    minWidth: "0"
});
globalStyle(".sc-shares-enc-salt", {
    minWidth: "0",
    maxWidth: "100%",
    overflowWrap: "anywhere"
});
globalStyle(".sc-shares-enc-announce", {
    minHeight: "1.25em",
    margin: "0",
    color: "rgb(var(--mdui-color-primary))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-shares-trash", {
    display: "inline-flex",
    alignItems: "center",
    gap: "8px",
    minHeight: "40px"
});
globalStyle(".sc-shares-trash-label", {
    fontSize: "var(--mdui-typescale-body-medium-size)",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    whiteSpace: "nowrap"
});
globalStyle(".sc-shares-enc-salt-row", {
    marginTop: "4px"
});
globalStyle(".sc-shares-enc-salt-label", {
    fontSize: "var(--mdui-typescale-body-small-size)",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-shares-enc-salt", {
    fontFamily: "ui-monospace, monospace",
    fontSize: "var(--mdui-typescale-body-small-size)",
    background: "rgb(var(--mdui-color-surface-container))",
    padding: "2px 6px",
    borderRadius: "var(--mdui-shape-corner-extra-small)"
});
globalStyle(".sc-share-backend", {
    maxWidth: "100%",
    marginInlineStart: "8px",
    paddingInline: "8px",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-label-small-size)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-share-actions", {
    display: "inline-flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "8px"
});
globalStyle(".sc-index-cost, .sc-admin-section-cost", {
    margin: "0",
    padding: "16px",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-medium)",
    background: "transparent",
    display: "flex",
    flexDirection: "column",
    gap: "12px"
});
globalStyle(".sc-index-cost dl, .sc-upload-estimate, .sc-admin-section-cost dl", {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(140px, 1fr))",
    gap: "16px",
    margin: "0"
});
globalStyle(".sc-index-cost dt, .sc-upload-estimate dt, .sc-admin-section-cost dt", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    marginBottom: "4px"
});
globalStyle(".sc-index-cost dd, .sc-upload-estimate dd, .sc-admin-section-cost dd", {
    margin: "0",
    fontSize: "var(--mdui-typescale-title-medium-size)",
    fontWeight: "600",
    color: "rgb(var(--mdui-color-on-surface))"
});
globalStyle(".sc-upload-estimate", {
    padding: "16px",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-medium)",
    background: "transparent"
});
globalStyle(".sc-upload-form, .sc-admin-section-upload-form", {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "16px",
    maxWidth: "36rem"
});
globalStyle(".sc-upload-form .sc-field, .sc-admin-section-upload-form .sc-field", {
    flex: "1 1 180px"
});
globalStyle(".sc-upload-actions, .sc-admin-section-upload-actions", {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "8px",
    flexShrink: "0"
});
globalStyle(".sc-picker", {
    display: "flex",
    flexDirection: "column",
    gap: "8px",
    width: "min(36rem, 100%)",
    minWidth: "0"
});
globalStyle(".sc-picker-nav", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    minWidth: "0"
});
globalStyle(".sc-picker-nav p", {
    minWidth: "0",
    margin: "0",
    overflowWrap: "anywhere",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-picker-body", {
    maxHeight: "40vh",
    overflowY: "auto",
    border: "1px solid var(--sc-outline-variant)",
    background: "transparent",
    borderRadius: "var(--sc-radius-small)"
});
globalStyle(".sc-picker-body p", {
    padding: "16px"
});
globalStyle(".sc-picker-body ul", {
    listStyle: "none",
    margin: "0",
    padding: "4px"
});
globalStyle(".sc-picker-body li button, .sc-picker-body li span", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    width: "100%",
    minBlockSize: "40px",
    padding: "8px",
    border: "0",
    background: "none",
    color: "inherit",
    textAlign: "left",
    overflowWrap: "anywhere"
});
globalStyle(".sc-admin-section-shares", {
    listStyle: "none",
    margin: "0",
    padding: "0",
    display: "flex",
    flexDirection: "column",
    gap: "4px"
});
globalStyle(".sc-admin-section-shares li", {
    display: "flex",
    justifyContent: "space-between",
    gap: "16px",
    maxWidth: "30rem",
    paddingBlock: "4px"
});
globalStyle(".sc-admin-section-share-label", {
    minWidth: "0",
    flex: "1 1 auto"
});
globalStyle(".sc-admin-section-share-usage", {
    flexShrink: "0",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-admin-section-saving, .sc-admin-section-upload-current", {
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-admin-section-upload-error", {
    color: "rgb(var(--mdui-color-error))"
});
globalStyle(".sc-admin-section-upload-saved", {
    color: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-admin-section-upload-current, .sc-admin-section-upload-error, .sc-admin-section-upload-saved", {
    margin: "8px 0 0"
});
globalStyle(".sc-path-row, .sc-upload-form, .sc-admin-section-upload-form", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "stretch",
            flexDirection: "column"
        }
    }
});
globalStyle(".sc-upload-form .sc-field, .sc-admin-section-upload-form .sc-field", {
    "@media": {
        "(max-width: 599.98px)": {
            flex: "none"
        }
    }
});
globalStyle(".sc-path-row > .sc-button-wrap", {
    "@media": {
        "(max-width: 599.98px)": {
            alignSelf: "flex-start"
        }
    }
});
globalStyle(".sc-storage-list li", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "flex-start",
            flexDirection: "column",
            gap: "8px"
        }
    }
});
globalStyle(".sc-storage-list strong", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "100%",
            textAlign: "start"
        }
    }
});
globalStyle(".sc-storage-toggle-row", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "flex-start",
            flexDirection: "column"
        }
    }
});
globalStyle(".sc-upload-actions, .sc-admin-section-upload-actions", {
    "@media": {
        "(max-width: 599.98px)": {
            flexWrap: "wrap"
        }
    }
});
globalStyle(".sc-shares-list .sc-list-item-trailing", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "100%",
            justifyContent: "flex-start"
        }
    }
});
globalStyle(".sc-picker-body li button, .sc-picker-body li span", {
    "@media": {
        "(max-width: 599.98px)": {
            minBlockSize: "44px"
        }
    }
});
