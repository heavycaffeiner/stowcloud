import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-search", {
    display: "flex",
    flexDirection: "column",
    gap: "12px",
    width: "100%",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-search-query-bar", {
    display: "flex",
    alignItems: "center",
    gap: "10px",
    height: "48px",
    padding: "0 12px 0 14px",
    background: "var(--sc-raised-surface)",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "12px",
    boxSizing: "border-box",
    transition: "border-color 150ms ease, box-shadow 150ms ease"
});
globalStyle(".sc-search-query-bar:focus-within", {
    borderColor: "rgb(var(--mdui-color-primary))",
    boxShadow: "0 0 0 2px rgba(var(--mdui-color-primary), 0.2)"
});
globalStyle(".sc-search-query-icon", {
    display: "inline-flex",
    alignItems: "center",
    color: "var(--sc-content-secondary)",
    flex: "none"
});
globalStyle(".sc-search-input", {
    flex: "1",
    minWidth: "0",
    height: "100%",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-primary)",
    fontFamily: "inherit",
    fontSize: "var(--mdui-typescale-body-large-size)",
    lineHeight: "var(--mdui-typescale-body-large-line-height)",
    outline: "none",
    padding: "0"
});
globalStyle(".sc-search-input::-webkit-search-decoration,\n.sc-search-input::-webkit-search-cancel-button,\n.sc-search-input::-webkit-search-results-button,\n.sc-search-input::-webkit-search-results-decoration", {
    WebkitAppearance: "none",
    display: "none"
});
globalStyle(".sc-search-input::placeholder", {
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-search-clear-btn", {
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    borderRadius: "50%",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-secondary)",
    cursor: "pointer",
    padding: "0",
    transition: "background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-search-clear-btn:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 10%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-search-submit-btn", {
    minHeight: "var(--sc-control-min)",
    padding: "0 10px",
    borderRadius: "var(--sc-radius-control, 10px)",
    border: "none",
    background: "transparent",
    color: "rgb(var(--mdui-color-primary))",
    fontFamily: "inherit",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    fontWeight: "500",
    cursor: "pointer",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    transition: "background-color 120ms ease, color 120ms ease"
});
globalStyle(".sc-search-submit-btn:hover,\n.sc-search-submit-btn:active", {
    background: "color-mix(in srgb, rgb(var(--mdui-color-primary)) 10%, transparent)",
    color: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-search-filter-bar", {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: "8px",
    padding: "2px 0 4px",
    overflow: "visible",
    scrollbarWidth: "none"
});
globalStyle(".sc-search-filter-bar::-webkit-scrollbar", {
    display: "none"
});
globalStyle(".sc-search-categories", {
    display: "flex",
    alignItems: "center",
    gap: "6px",
    flex: "1 1 auto",
    overflowX: "auto",
    scrollbarWidth: "none"
});
globalStyle(".sc-search-categories::-webkit-scrollbar", {
    display: "none"
});
globalStyle(".sc-search-category-pill", {
    display: "inline-flex",
    alignItems: "center",
    gap: "6px",
    minHeight: "var(--sc-control-min)",
    padding: "0 12px",
    borderRadius: "20px",
    background: "color-mix(in srgb, var(--sc-content-primary) 5%, transparent)",
    border: "1px solid var(--sc-outline-variant)",
    color: "var(--sc-content-secondary)",
    fontFamily: "inherit",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    fontWeight: "500",
    cursor: "pointer",
    whiteSpace: "nowrap",
    flex: "none",
    transition: "background-color 140ms ease, border-color 140ms ease, color 140ms ease"
});
globalStyle(".sc-search-category-pill:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 10%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-search-category-pill-active", {
    background: "var(--sc-state-selection)",
    borderColor: "color-mix(in srgb, var(--sc-state-selection-content) 35%, transparent)",
    color: "var(--sc-state-selection-content)",
    fontWeight: "600"
});
globalStyle(".sc-search-category-pill-active:hover", {
    background: "color-mix(in srgb, var(--sc-state-selection) 88%, var(--sc-content-primary))",
    color: "var(--sc-state-selection-content)"
});
globalStyle(".sc-search-scope-pill", {
    display: "inline-flex",
    alignItems: "center",
    gap: "5px",
    height: "30px",
    padding: "0 10px",
    borderRadius: "15px",
    background: "color-mix(in srgb, var(--sc-state-selection) 55%, transparent)",
    border: "1px solid color-mix(in srgb, var(--sc-state-selection-content) 25%, transparent)",
    color: "var(--sc-state-selection-content)",
    fontSize: "var(--mdui-typescale-label-medium-size)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height, 1rem)",
    fontWeight: "500",
    whiteSpace: "nowrap",
    flex: "none"
});
globalStyle(".sc-search-sort-wrap", {
    position: "relative",
    flex: "none"
});
globalStyle(".sc-search-sort-btn", {
    display: "inline-flex",
    alignItems: "center",
    gap: "6px",
    minHeight: "var(--sc-control-min)",
    padding: "0 10px",
    borderRadius: "20px",
    background: "transparent",
    border: "1px solid var(--sc-outline-variant)",
    color: "var(--sc-content-secondary)",
    fontFamily: "inherit",
    fontSize: "var(--mdui-typescale-label-medium-size)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height, 1rem)",
    fontWeight: "500",
    cursor: "pointer",
    whiteSpace: "nowrap",
    transition: "background-color 140ms ease, color 140ms ease"
});
globalStyle(".sc-search-clear-btn:focus-visible,\n.sc-search-submit-btn:focus-visible,\n.sc-search-sort-btn:focus-visible,\n.sc-search-menu button:focus-visible,\n.sc-search-stop-btn:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-search-category-pill:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-inset-offset)"
});
globalStyle(".sc-search-sort-btn:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 8%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-search-menu", {
    position: "absolute",
    top: "calc(100% + 4px)",
    right: "0",
    zIndex: "50",
    minWidth: "140px",
    padding: "4px",
    background: "var(--sc-overlay-surface)",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "10px",
    boxShadow: "0 8px 24px rgba(0, 0, 0, 0.28)",
    display: "flex",
    flexDirection: "column",
    gap: "2px"
});
globalStyle(".sc-search-menu button", {
    display: "flex",
    alignItems: "center",
    minHeight: "var(--sc-control-min)",
    padding: "8px 12px",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-primary)",
    fontFamily: "inherit",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height)",
    textAlign: "left",
    borderRadius: "6px",
    cursor: "pointer",
    transition: "background-color 120ms ease"
});
globalStyle(".sc-search-menu button:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 8%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-search-status-bar", {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    minHeight: "24px",
    padding: "0 2px",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-search-status-info", {
    display: "inline-flex",
    alignItems: "center",
    gap: "6px"
});
globalStyle(".sc-search-progress", {
    display: "inline-flex",
    alignItems: "center",
    width: "14px",
    height: "14px"
});
globalStyle(".sc-search-progress mdui-circular-progress", {
    width: "14px",
    height: "14px"
});
globalStyle(".sc-search-status-actions", {
    display: "flex",
    alignItems: "center",
    gap: "8px"
});
globalStyle(".sc-search-stop-btn", {
    minHeight: "var(--sc-control-min)",
    display: "inline-flex",
    alignItems: "center",
    background: "transparent",
    border: "none",
    color: "rgb(var(--mdui-color-error))",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height)",
    cursor: "pointer",
    padding: "0 8px",
    borderRadius: "8px"
});
globalStyle(".sc-search-stop-btn:hover", {
    background: "color-mix(in srgb, rgb(var(--mdui-color-error)) 12%, transparent)"
});
globalStyle(".sc-search-spoken", {
    position: "absolute",
    width: "1px",
    height: "1px",
    overflow: "hidden",
    clip: "rect(0 0 0 0)"
});
globalStyle(".sc-search-results", {
    position: "relative",
    maxHeight: "420px",
    overflowY: "auto",
    overflowX: "hidden",
    borderRadius: "10px",
    background: "var(--sc-page-surface)",
    border: "1px solid var(--sc-outline-variant)",
    outline: "none"
});
globalStyle(".sc-search-note", {
    padding: "32px 16px",
    textAlign: "center",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height)",
    margin: "0"
});
globalStyle(".sc-search-spacer", {
    position: "relative"
});
globalStyle(".sc-search-rows", {
    listStyle: "none",
    margin: "0",
    padding: "0",
    width: "100%"
});
globalStyle(".sc-search-row", {
    width: "100%",
    height: "56px",
    display: "flex",
    alignItems: "center",
    gap: "12px",
    padding: "0 16px",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-primary)",
    cursor: "pointer",
    textAlign: "left",
    boxSizing: "border-box",
    transition: "background-color 120ms ease"
});
globalStyle(".sc-search-row:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 5%, transparent)"
});
globalStyle(".sc-search-row:focus-visible", {
    background: "color-mix(in srgb, var(--sc-content-primary) 8%, transparent)",
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-inset-offset)"
});
globalStyle(".sc-search-row-icon", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    width: "32px",
    height: "32px",
    borderRadius: "8px",
    background: "color-mix(in srgb, var(--sc-content-primary) 6%, transparent)",
    flex: "none"
});
globalStyle(".sc-search-text", {
    flex: "1",
    minWidth: "0",
    display: "flex",
    flexDirection: "column",
    gap: "2px"
});
globalStyle(".sc-search-name", {
    fontSize: "var(--mdui-typescale-body-large-size)",
    lineHeight: "var(--mdui-typescale-body-large-line-height)",
    fontWeight: "500",
    color: "var(--sc-content-primary)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-search-folder", {
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    color: "var(--sc-content-secondary)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-search-cell", {
    display: "flex",
    flexDirection: "column",
    alignItems: "flex-end",
    gap: "2px",
    flex: "none"
});
globalStyle(".sc-search-size", {
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    fontWeight: "500",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-search-date", {
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-search-query-bar", {
    "@media": {
        "(max-width: 599.98px)": {
            height: "56px",
            paddingBlock: "5px"
        }
    }
});
globalStyle(".sc-search-filter-bar", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "stretch",
            flexDirection: "column",
            gap: "8px",
            overflow: "visible"
        }
    }
});
globalStyle(".sc-search-categories", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "100%",
            flex: "none",
            paddingBottom: "2px",
            overscrollBehaviorInline: "contain",
            scrollSnapType: "x proximity"
        }
    }
});
globalStyle(".sc-search-category-pill", {
    "@media": {
        "(max-width: 599.98px)": {
            minHeight: "44px",
            scrollSnapAlign: "start"
        }
    }
});
globalStyle(".sc-search-scope-pill", {
    "@media": {
        "(max-width: 599.98px)": {
            alignSelf: "flex-start",
            minHeight: "32px"
        }
    }
});
globalStyle(".sc-search-sort-wrap,\n  .sc-search-sort-btn", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "100%"
        }
    }
});
globalStyle(".sc-search-sort-btn", {
    "@media": {
        "(max-width: 599.98px)": {
            minHeight: "44px",
            justifyContent: "center"
        }
    }
});
globalStyle(".sc-search-status-bar", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "stretch",
            flexDirection: "column",
            gap: "8px"
        }
    }
});
globalStyle(".sc-search-status-actions", {
    "@media": {
        "(max-width: 599.98px)": {
            justifyContent: "flex-end"
        }
    }
});
globalStyle(".sc-search-stop-btn", {
    "@media": {
        "(max-width: 599.98px)": {
            minHeight: "44px",
            paddingInline: "14px"
        }
    }
});
globalStyle(".sc-search-results", {
    "@media": {
        "(max-width: 599.98px)": {
            maxHeight: "none",
            minHeight: "240px"
        }
    }
});
globalStyle(".sc-search-row", {
    "@media": {
        "(max-width: 599.98px)": {
            height: "auto",
            minHeight: "64px",
            padding: "10px 12px",
            gap: "10px"
        }
    }
});
globalStyle(".sc-search-cell", {
    "@media": {
        "(max-width: 599.98px)": {
            display: "none"
        }
    }
});
