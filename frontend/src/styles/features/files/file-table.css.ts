import { globalKeyframes, globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-file-table", {
    position: "relative",
    flex: "1",
    minWidth: "0",
    alignSelf: "flex-start",
    contain: "content",
    background: "var(--sc-page-surface, var(--mdui-color-surface, var(--m3c-surface)))",
    color: "var(--sc-content-primary, var(--mdui-color-on-surface, var(--m3c-on-surface)))",
    containerType: "inline-size",
    containerName: "sc-file-table",
    paddingBottom: "calc(24px + var(--sc-reserve-selection, 0px))",
    vars: { "--sc-modified-column-width": "11rem" }
});
globalStyle(".sc-file-table:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-inset-offset)"
});
globalStyle(".sc-file-table-contained", {
    alignSelf: "stretch",
    minHeight: "0",
    overflow: "auto",
    overscrollBehaviorY: "contain"
});
globalStyle(".sc-file-table-reserve-selection", {
    vars: { "--sc-reserve-selection": "80px" }
});
globalStyle(".sc-file-table-header", {
    position: "sticky",
    zIndex: "2",
    top: "0",
    display: "flex",
    alignItems: "center",
    height: "40px",
    paddingInline: "var(--sc-content-pad, 24px)",
    borderBottom: "none",
    background: "color-mix(in srgb, var(--sc-container-surface) 70%, var(--sc-page-surface))",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-label-large-size, .875rem)",
    fontWeight: "var(--mdui-typescale-label-large-weight, 500)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    letterSpacing: "var(--mdui-typescale-label-large-tracking, .006rem)"
});
globalStyle(".sc-file-table-header-cell", {
    display: "flex",
    alignItems: "center",
    minWidth: "0",
    gap: "4px",
    overflow: "hidden"
});
globalStyle(".sc-file-table-header-cell-select", {
    flex: "0 0 40px",
    justifyContent: "center",
    border: "0",
    background: "transparent",
    padding: "0",
    color: "inherit",
    cursor: "pointer"
});
globalStyle(".sc-file-table-header-cell-name", {
    flex: "1 1 auto"
});
globalStyle(".sc-file-table-header-cell-size", {
    flex: "0 0 112px",
    justifyContent: "flex-end"
});
globalStyle(".sc-file-table-header-cell-mtime", {
    flex: "0 0 var(--sc-modified-column-width)",
    justifyContent: "flex-end",
    whiteSpace: "nowrap"
});
globalStyle(".sc-file-table-header-cell-actions", {
    flex: "0 0 40px"
});
globalStyle(".sc-file-table-header-button", {
    display: "inline-flex",
    alignItems: "center",
    gap: "4px",
    minWidth: "0",
    minHeight: "var(--sc-control-min-desktop)",
    padding: "6px 4px",
    border: "0",
    borderRadius: "var(--mdui-shape-corner-extra-small, 4px)",
    background: "transparent",
    color: "inherit",
    font: "inherit",
    cursor: "pointer",
    transition: "background-color 120ms ease, color 120ms ease, transform 100ms ease"
});
globalStyle(".sc-file-table-header-button:active", {
    transform: "scale(0.96)"
});
globalStyle(".sc-file-table-header-button:hover", {
    background: "color-mix(in srgb, currentColor 10%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-file-table-header-button:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-file-table-header-button-active", {
    color: "rgb(var(--mdui-color-primary))",
    fontWeight: "600"
});
globalStyle(".sc-file-table-header-sort", {
    display: "inline-flex",
    flex: "none",
    fontWeight: "700"
});
globalStyle(".sc-file-table-spacer", {
    position: "relative"
});
globalStyle(".sc-file-table-window", {
    willChange: "transform"
});
globalStyle(".sc-file-table-empty", {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    minHeight: "160px",
    padding: "64px 24px",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-large-size, 1rem)"
});
globalStyle(".sc-row", {
    display: "flex",
    alignItems: "center",
    height: "var(--sc-row-height, 48px)",
    paddingInline: "var(--sc-content-pad, 24px)",
    cursor: "pointer",
    borderBottom: "none",
    color: "var(--sc-content-primary)",
    fontSize: "var(--mdui-typescale-body-large-size, 1rem)",
    fontWeight: "var(--mdui-typescale-body-large-weight, 400)",
    lineHeight: "var(--mdui-typescale-body-large-line-height, 1.5rem)",
    userSelect: "none",
    WebkitUserSelect: "none",
    transition: "background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-row:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 5%, transparent)"
});
globalStyle(".sc-row-selected", {
    background: "var(--sc-state-selection)",
    color: "var(--sc-state-selection-content)"
});
globalStyle(".sc-row-selected:hover", {
    background: "color-mix(in srgb, var(--sc-state-selection) 85%, var(--sc-content-primary))"
});
globalStyle(".sc-row-focused", {
    outline: "none"
});
globalStyle(".sc-file-table:focus-visible .sc-row-focused", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-inset-offset)"
});
globalStyle(".sc-row-cell", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    overflow: "hidden"
});
globalStyle(".sc-row-cell-select", {
    flex: "0 0 40px",
    minHeight: "var(--sc-control-min-desktop)",
    overflow: "visible",
    justifyContent: "center"
});
globalStyle(".sc-row-cell-name", {
    flex: "1 1 auto",
    minWidth: "0"
});
globalStyle(".sc-row-cell-size", {
    flex: "0 0 112px",
    justifyContent: "flex-end",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-medium-size, .875rem)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height, 1.25rem)"
});
globalStyle(".sc-row-cell-mtime", {
    flex: "0 0 var(--sc-modified-column-width)",
    justifyContent: "flex-end",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-medium-size, .875rem)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height, 1.25rem)",
    fontVariantNumeric: "tabular-nums",
    whiteSpace: "nowrap"
});
globalStyle(".sc-row-cell-actions", {
    flex: "0 0 40px",
    justifyContent: "center",
    overflow: "visible"
});
globalStyle(".sc-row-icon-badge", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    flex: "none",
    width: "22px"
});
globalStyle(".sc-row-name-copy", {
    display: "flex",
    flex: "1 1 auto",
    minWidth: "0"
});
globalStyle(".sc-row-cell-name .sc-filename", {
    flex: "1 1 auto",
    overflow: "hidden"
});
globalStyle(".sc-row-mobile-meta", {
    display: "none"
});
globalStyle(".sc-row-badge", {
    display: "inline-flex",
    flex: "none",
    color: "rgb(var(--mdui-color-error))"
});
globalStyle(".sc-row-more-btn", {
    width: "var(--sc-control-min-desktop)",
    height: "var(--sc-control-min-desktop)",
    borderRadius: "50%",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-secondary)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    cursor: "pointer",
    opacity: "0",
    padding: "0",
    transition: "opacity 120ms ease, background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-row:hover .sc-row-more-btn,\n.sc-row-selected .sc-row-more-btn,\n.sc-row-focused .sc-row-more-btn", {
    opacity: "1"
});
globalStyle(".sc-row-more-btn:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-row-more-btn:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 10%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-custom-checkbox", {
    width: "18px",
    height: "18px",
    borderRadius: "4px",
    border: "1.5px solid var(--sc-outline)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    background: "transparent",
    color: "rgb(var(--mdui-color-on-primary))",
    boxSizing: "border-box",
    cursor: "pointer",
    transition: "background-color 120ms ease, border-color 120ms ease"
});
globalStyle(".sc-custom-checkbox-checked", {
    background: "rgb(var(--mdui-color-primary))",
    borderColor: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-custom-checkbox-indeterminate", {
    background: "rgb(var(--mdui-color-primary))",
    borderColor: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-custom-checkbox-bar", {
    width: "10px",
    height: "2px",
    borderRadius: "1px",
    background: "rgb(var(--mdui-color-on-primary))"
});
globalStyle(".sc-file-table-header-cell-mtime, .sc-row-cell-mtime", {
    "@container": {
        "sc-file-table (max-width: 599.98px)": {
            display: "none"
        }
    }
});
globalStyle(".sc-file-table-header-cell-size, .sc-row-cell-size", {
    "@container": {
        "sc-file-table (max-width: 599.98px)": {
            flexBasis: "88px"
        }
    }
});
globalStyle(".sc-file-table-header", {
    "@container": {
        "sc-file-table (max-width: 599.98px)": {
            height: "40px",
            paddingInline: "16px"
        }
    }
});
globalStyle(".sc-row", {
    "@container": {
        "sc-file-table (max-width: 599.98px)": {
            paddingInline: "16px"
        }
    }
});
globalStyle(".sc-row-skeleton", {
    display: "flex",
    alignItems: "center",
    height: "var(--sc-row-height, 48px)",
    paddingInline: "16px",
    borderBottom: "none"
});
globalStyle(".sc-row-skeleton-cell", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    overflow: "hidden"
});
globalStyle(".sc-row-skeleton-cell-select", {
    flex: "0 0 36px"
});
globalStyle(".sc-row-skeleton-cell-name", {
    flex: "1 1 auto",
    minWidth: "0"
});
globalStyle(".sc-row-skeleton-cell-size", {
    flex: "0 0 112px",
    justifyContent: "flex-end"
});
globalStyle(".sc-row-skeleton-cell-mtime", {
    flex: "0 0 var(--sc-modified-column-width)",
    justifyContent: "flex-end"
});
globalStyle(".sc-row-skeleton-bar", {
    display: "block",
    height: "16px",
    borderRadius: "var(--mdui-shape-corner-extra-small, 4px)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    animation: "sc-row-skeleton-pulse 1.2s ease-in-out infinite"
});
globalStyle(".sc-row-skeleton-bar-icon", {
    flex: "0 0 20px",
    height: "20px",
    borderRadius: "var(--mdui-shape-corner-small, 8px)"
});
globalStyle(".sc-row-skeleton-bar-name", {
    width: "60%"
});
globalStyle(".sc-row-skeleton-bar-size", {
    width: "40px"
});
globalStyle(".sc-row-skeleton-bar-mtime", {
    width: "88px"
});
globalStyle(".sc-row-skeleton-cell-mtime", {
    "@container": {
        "sc-file-table (max-width: 599.98px)": {
            display: "none"
        }
    }
});
globalStyle(".sc-row-skeleton-cell-size", {
    "@container": {
        "sc-file-table (max-width: 599.98px)": {
            flexBasis: "88px"
        }
    }
});
globalStyle(".sc-row-skeleton", {
    "@container": {
        "sc-file-table (max-width: 599.98px)": {
            paddingInline: "12px"
        }
    }
});
globalKeyframes("sc-row-skeleton-pulse", {
    "0%, 100%": {
        opacity: ".5"
    },
    "50%": {
        opacity: "1"
    }
});
globalStyle(".sc-row, .sc-row-skeleton-bar", {
    "@media": {
        "(prefers-reduced-motion: reduce)": {
            transition: "none",
            animation: "none"
        }
    }
});
globalStyle(".sc-file-table[data-density='compact'] .sc-row, .sc-file-table[data-density='compact'] .sc-row-skeleton", {
    height: "40px"
});
globalStyle(".sc-file-table[data-density='comfortable'] .sc-row, .sc-file-table[data-density='comfortable'] .sc-row-skeleton", {
    height: "48px"
});
globalStyle(".sc-file-table[data-density='spacious'] .sc-row, .sc-file-table[data-density='spacious'] .sc-row-skeleton", {
    height: "56px"
});
globalStyle(".sc-file-table-mobile-rows .sc-file-table-header-cell-size,\n.sc-file-table-mobile-rows .sc-file-table-header-cell-mtime,\n.sc-file-table-mobile-rows .sc-row-cell-size,\n.sc-file-table-mobile-rows .sc-row-cell-mtime,\n.sc-file-table-mobile-rows .sc-row-skeleton-cell-size,\n.sc-file-table-mobile-rows .sc-row-skeleton-cell-mtime", {
    display: "none"
});
globalStyle(".sc-file-table-mobile-rows .sc-file-table-header", {
    paddingInline: "12px"
});
globalStyle(".sc-file-table-mobile-rows .sc-file-table-header-cell-select", {
    flexBasis: "44px"
});
globalStyle(".sc-file-table-mobile-rows .sc-file-table-header-cell-actions", {
    flexBasis: "44px"
});
globalStyle(".sc-file-table.sc-file-table-mobile-rows .sc-row,\n.sc-file-table.sc-file-table-mobile-rows .sc-row-skeleton", {
    height: "64px",
    paddingInline: "12px"
});
globalStyle(".sc-file-table-mobile-rows .sc-row", {
    display: "grid",
    gridTemplateColumns: "44px minmax(0, 1fr) 44px",
    alignItems: "center"
});
globalStyle(".sc-file-table-mobile-rows .sc-row-cell-select", {
    gridColumn: "1",
    width: "44px",
    height: "44px",
    justifyContent: "center",
    flex: "none"
});
globalStyle(".sc-file-table-mobile-rows .sc-row-cell-name", {
    gridColumn: "2",
    minWidth: "0"
});
globalStyle(".sc-file-table-mobile-rows .sc-row-name-copy", {
    flexDirection: "column",
    justifyContent: "center",
    gap: "1px",
    minWidth: "0"
});
globalStyle(".sc-file-table-mobile-rows .sc-row-cell-name .sc-filename", {
    width: "100%",
    lineHeight: "var(--mdui-typescale-body-large-line-height, 1.5rem)"
});
globalStyle(".sc-file-table-mobile-rows .sc-row-mobile-meta", {
    display: "block",
    maxWidth: "100%",
    overflow: "hidden",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-small-size, .75rem)",
    lineHeight: "var(--mdui-typescale-body-small-line-height, 1rem)",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-file-table-mobile-rows .sc-row-selected .sc-row-mobile-meta", {
    color: "inherit",
    opacity: ".78"
});
globalStyle(".sc-file-table-mobile-rows .sc-row-cell-actions", {
    gridColumn: "3",
    width: "44px",
    height: "44px",
    justifyContent: "center",
    flex: "none"
});
globalStyle(".sc-file-table-mobile-rows .sc-row-more-btn", {
    width: "44px",
    height: "44px",
    opacity: "1"
});
globalStyle(".sc-file-table-mobile-rows .sc-row-skeleton-cell-select", {
    flexBasis: "44px"
});
