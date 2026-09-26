import { globalKeyframes, globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-file-grid", {
    position: "relative",
    flex: "1",
    minWidth: "0",
    alignSelf: "flex-start",
    contain: "content",
    background: "var(--sc-page-surface, rgb(var(--mdui-color-surface)))",
    containerType: "inline-size",
    containerName: "sc-file-grid",
    paddingBottom: "calc(24px + var(--sc-reserve-selection, 0px))"
});
globalStyle(".sc-file-grid:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-inset-offset)"
});
globalStyle(".sc-file-grid-contained", {
    alignSelf: "stretch",
    minHeight: "0",
    overflow: "auto",
    overscrollBehaviorY: "contain"
});
globalStyle(".sc-file-grid-reserve-selection", {
    vars: { "--sc-reserve-selection": "80px" }
});
globalStyle(".sc-file-grid-group", {
    margin: "0",
    padding: "16px var(--sc-content-pad, 24px) 8px",
    color: "var(--sc-content-secondary, rgb(var(--mdui-color-on-surface-variant)))",
    fontSize: "var(--mdui-typescale-title-small-size, 0.875rem)",
    fontWeight: "var(--mdui-typescale-title-small-weight, 500)",
    lineHeight: "var(--mdui-typescale-title-small-line-height, 1.25rem)",
    letterSpacing: "var(--mdui-typescale-title-small-tracking, 0.007rem)"
});
globalStyle(".sc-file-grid-section", {
    position: "relative"
});
globalStyle(".sc-file-grid-window", {
    willChange: "transform",
    paddingInline: "var(--sc-content-pad, 24px)"
});
globalStyle(".sc-file-grid-row", {
    display: "flex",
    alignItems: "stretch",
    gap: "16px"
});
globalStyle(".sc-file-grid-card", {
    minWidth: "0",
    position: "relative",
    boxSizing: "border-box",
    height: "100%",
    border: "none",
    borderRadius: "var(--sc-radius-card, 14px)",
    background: "var(--sc-container-surface, rgb(var(--mdui-color-surface-container)))",
    color: "var(--sc-content-primary, rgb(var(--mdui-color-on-surface)))",
    cursor: "pointer",
    overflow: "hidden",
    userSelect: "none",
    WebkitUserSelect: "none",
    boxShadow: "var(--sc-shadow-card, 0 1px 2px rgba(0, 0, 0, 0.06))",
    transition: "transform 160ms cubic-bezier(0.2, 0, 0, 1),\n              background-color 150ms ease,\n              box-shadow 160ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-file-grid-card-folder", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    paddingInline: "8px"
});
globalStyle(".sc-file-grid-card-file", {
    display: "flex",
    flexDirection: "column"
});
globalStyle(".sc-file-grid-card:hover", {
    transform: "translateY(-1px)",
    boxShadow: "0 3px 10px rgba(0, 0, 0, 0.10)"
});
globalStyle(".sc-file-grid-card:not(.sc-file-grid-card-selected):hover", {
    background: "var(--sc-raised-surface, rgb(var(--mdui-color-surface-container-high)))"
});
globalStyle(".sc-file-grid-card:active", {
    transform: "scale(0.98)"
});
globalStyle(".sc-file-grid-card-selected", {
    background: "var(--sc-state-selection, rgb(var(--mdui-color-secondary-container)))",
    color: "var(--sc-state-selection-content, rgb(var(--mdui-color-on-secondary-container)))",
    border: "none",
    boxShadow: "var(--sc-shadow-card, 0 1px 2px rgba(0, 0, 0, 0.06))"
});
globalStyle(".sc-file-grid:focus-visible .sc-file-grid-card-focused", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-inset-offset)"
});
globalStyle(".sc-file-grid-head", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    padding: "4px 8px",
    minHeight: "40px"
});
globalStyle(".sc-file-grid-type", {
    display: "inline-flex",
    flex: "none",
    alignItems: "center",
    justifyContent: "center",
    color: "var(--sc-icon-color)"
});
globalStyle(".sc-file-grid-name", {
    flex: "1",
    overflow: "hidden",
    fontSize: "var(--mdui-typescale-body-medium-size, 0.875rem)",
    fontWeight: "var(--mdui-typescale-body-medium-weight, 400)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height, 1.25rem)",
    letterSpacing: "var(--mdui-typescale-body-medium-tracking, 0.016rem)"
});
globalStyle(".sc-file-grid-thumb", {
    flex: "1",
    minHeight: "0",
    margin: "0 8px",
    borderRadius: "var(--mdui-shape-corner-small, 8px)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    overflow: "hidden"
});
globalStyle(".sc-file-grid-thumb img", {
    width: "100%",
    height: "100%",
    objectFit: "cover",
    transition: "transform 220ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-file-grid-card:hover .sc-file-grid-thumb img", {
    transform: "scale(1.04)"
});
globalStyle(".sc-file-grid-meta", {
    padding: "4px 8px 8px",
    color: "var(--sc-content-secondary, rgb(var(--mdui-color-on-surface-variant)))",
    fontSize: "var(--mdui-typescale-body-small-size, 0.75rem)",
    fontWeight: "var(--mdui-typescale-body-small-weight, 400)",
    lineHeight: "var(--mdui-typescale-body-small-line-height, 1rem)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking, 0.025rem)"
});
globalStyle(".sc-file-grid-card-selected .sc-file-grid-meta", {
    color: "inherit"
});
globalStyle(".sc-file-grid-check", {
    display: "inline-flex",
    flex: "none",
    alignItems: "center",
    justifyContent: "center",
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)"
});
globalStyle(".sc-file-grid-check mdui-checkbox", {
    pointerEvents: "none"
});
globalStyle(".sc-file-grid-kebab", {
    display: "inline-flex",
    flex: "none",
    alignItems: "center",
    justifyContent: "center",
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    padding: "0",
    border: "none",
    borderRadius: "50%",
    background: "none",
    color: "inherit",
    cursor: "pointer",
    transition: "background-color 120ms ease, transform 100ms ease"
});
globalStyle(".sc-file-grid-kebab:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-file-grid-kebab:hover", {
    background: "color-mix(in srgb, currentColor 12%, transparent)"
});
globalStyle(".sc-file-grid-kebab:active", {
    transform: "scale(0.92)"
});
globalStyle(".sc-file-grid-badge", {
    display: "inline-flex",
    flex: "none",
    color: "rgb(var(--mdui-color-error))"
});
globalStyle(".sc-file-grid-skeleton", {
    display: "flex",
    flexDirection: "column",
    gap: "8px",
    padding: "8px",
    cursor: "default"
});
globalStyle(".sc-file-grid-skeleton-line", {
    height: "12px",
    borderRadius: "var(--mdui-shape-corner-extra-small, 4px)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    animation: "sc-file-grid-pulse 1.2s ease-in-out infinite"
});
globalStyle(".sc-file-grid-skeleton-block", {
    flex: "1",
    borderRadius: "var(--mdui-shape-corner-small, 8px)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    animation: "sc-file-grid-pulse 1.2s ease-in-out infinite"
});
globalKeyframes("sc-file-grid-pulse", {
    "0%, 100%": {
        opacity: ".5"
    },
    "50%": {
        opacity: "1"
    }
});
globalStyle(".sc-file-grid-skeleton-line, .sc-file-grid-skeleton-block, .sc-file-grid-card", {
    "@media": {
        "(prefers-reduced-motion: reduce)": {
            animation: "none",
            transition: "none"
        }
    }
});
globalStyle(".sc-file-grid-empty", {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    paddingBlock: "64px",
    color: "var(--sc-content-secondary, rgb(var(--mdui-color-on-surface-variant)))"
});
globalStyle(".sc-file-grid[data-density='compact'] .sc-file-grid-card-folder", {
    height: "44px"
});
globalStyle(".sc-file-grid[data-density='compact'] .sc-file-grid-card-file", {
    height: "176px"
});
globalStyle(".sc-file-grid[data-density='comfortable'] .sc-file-grid-card-folder", {
    height: "52px"
});
globalStyle(".sc-file-grid[data-density='comfortable'] .sc-file-grid-card-file", {
    height: "208px"
});
globalStyle(".sc-file-grid[data-density='spacious'] .sc-file-grid-card-folder", {
    height: "60px"
});
globalStyle(".sc-file-grid[data-density='spacious'] .sc-file-grid-card-file", {
    height: "244px"
});
