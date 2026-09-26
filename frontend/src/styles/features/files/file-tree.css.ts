import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-file-tree", {
    flex: "0 0 240px",
    width: "240px",
    overflowY: "auto",
    paddingBlock: "8px",
    borderInlineEnd: "none",
    background: "rgb(var(--mdui-color-surface-container-low))",
    color: "var(--sc-content-primary, rgb(var(--mdui-color-on-surface)))"
});
globalStyle("ul[role='tree']", {
    listStyle: "none",
    margin: "0",
    padding: "0"
});
globalStyle(".sc-file-tree-list", {
    minWidth: "0"
});
globalStyle(".sc-file-tree-list > ul", {
    width: "100%"
});
globalStyle(".sc-file-tree", {
    "@container": {
        "sc-browse-content (max-width: 839.98px)": {
            flexBasis: "200px",
            width: "200px"
        }
    }
});
globalStyle(".sc-file-tree--overlay", {
    position: "fixed",
    top: "0",
    bottom: "calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px))",
    insetInlineStart: "0",
    margin: "0",
    maxWidth: "min(320px, 85vw)",
    width: "100%",
    height: "auto",
    padding: "0",
    border: "none",
    boxShadow: "var(--mdui-elevation-level2, 0 3px 6px rgb(0 0 0 / .24))",
    translate: "0 0",
    transition: "translate var(--mdui-motion-easing-standard, cubic-bezier(.2,0,0,1)), display var(--mdui-motion-duration-medium2, .25s) allow-discrete, overlay var(--mdui-motion-duration-medium2, .25s) allow-discrete"
});
globalStyle(".sc-file-tree--overlay:not([open])", {
    translate: "-100% 0"
});
globalStyle(".sc-file-tree--overlay[open]", {
    "@starting-style": {
        translate: "-100% 0"
    }
});
globalStyle(".sc-file-tree--overlay::backdrop", {
    background: "color-mix(in srgb, rgb(var(--mdui-color-scrim)) 32%, transparent)",
    transition: "background-color var(--mdui-motion-easing-standard, cubic-bezier(.2,0,0,1)), display var(--mdui-motion-duration-medium2, .25s) allow-discrete, overlay var(--mdui-motion-duration-medium2, .25s) allow-discrete"
});
globalStyle(".sc-file-tree--overlay:not([open])::backdrop", {
    background: "color-mix(in srgb, rgb(var(--mdui-color-scrim)) 0%, transparent)"
});
globalStyle(".sc-file-tree--overlay .sc-tree-row", {
    height: "44px"
});
globalStyle(".sc-file-tree--overlay .sc-tree-row-more,\n.sc-file-tree--overlay .sc-tree-row-status", {
    minHeight: "44px"
});
globalStyle(".sc-file-tree--overlay[open]::backdrop", {
    "@starting-style": {
        background: "color-mix(in srgb, rgb(var(--mdui-color-scrim)) 0%, transparent)"
    }
});
globalStyle(".sc-file-tree-overlay-header", {
    position: "sticky",
    top: "0",
    zIndex: "1",
    display: "flex",
    alignItems: "center",
    justifyContent: "flex-end",
    height: "56px",
    paddingInline: "8px",
    boxShadow: "0 1px 0 rgb(var(--mdui-color-outline-variant))",
    background: "var(--sc-page-surface, rgb(var(--mdui-color-surface)))"
});
globalStyle(".sc-file-tree-overlay-header button", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    padding: "0",
    border: "0",
    borderRadius: "50%",
    background: "transparent",
    color: "inherit",
    fontSize: "1.5rem",
    lineHeight: "1",
    cursor: "pointer"
});
globalStyle(".sc-tree-row", {
    display: "flex",
    alignItems: "center",
    height: "40px",
    borderRadius: "var(--mdui-shape-corner-full, 999px)",
    color: "var(--sc-content-primary, rgb(var(--mdui-color-on-surface)))",
    transition: "background-color 150ms ease, color 150ms ease"
});
globalStyle(".sc-tree-row--ancestor", {
    background: "color-mix(in srgb, rgb(var(--mdui-color-secondary-container)) 40%, transparent)"
});
globalStyle(".sc-tree-row--active", {
    background: "var(--sc-state-selection, rgb(var(--mdui-color-secondary-container)))",
    color: "var(--sc-state-selection-content, rgb(var(--mdui-color-on-secondary-container)))",
    fontWeight: "600"
});
globalStyle(".sc-tree-row-label > svg", {
    color: "var(--sc-icon-color)"
});
globalStyle(".sc-tree-row-twisty", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    flex: "0 0 var(--sc-control-min)",
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    padding: "0",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-full, 999px)",
    background: "transparent",
    color: "var(--sc-content-secondary, rgb(var(--mdui-color-on-surface-variant)))",
    cursor: "pointer",
    transition: "background-color 150ms ease"
});
globalStyle(".sc-tree-row-twisty:hover", {
    background: "color-mix(in srgb, currentColor 8%, transparent)"
});
globalStyle(".sc-tree-row-twisty-icon", {
    display: "inline-flex",
    transition: "transform 150ms ease"
});
globalStyle(".sc-tree-row-twisty-icon--expanded", {
    transform: "rotate(90deg)"
});
globalStyle(".sc-tree-row-label", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    flex: "1",
    minWidth: "0",
    height: "100%",
    paddingInline: "0 8px",
    border: "none",
    background: "transparent",
    color: "inherit",
    fontSize: "var(--mdui-typescale-body-medium-size, .875rem)",
    fontWeight: "var(--mdui-typescale-body-medium-weight, 400)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height, 1.25rem)",
    letterSpacing: "var(--mdui-typescale-body-medium-tracking, .016rem)",
    textAlign: "start",
    cursor: "pointer"
});
globalStyle(".sc-tree-row-name", {
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-tree-row-status", {
    display: "flex",
    alignItems: "center",
    minHeight: "28px",
    margin: "0",
    paddingBlock: "4px",
    overflowWrap: "anywhere",
    color: "var(--sc-content-secondary, rgb(var(--mdui-color-on-surface-variant)))",
    fontSize: "var(--mdui-typescale-body-small-size, .75rem)",
    lineHeight: "var(--mdui-typescale-body-small-line-height, 1rem)"
});
globalStyle(".sc-tree-row-status--error", {
    color: "rgb(var(--mdui-color-error))"
});
globalStyle(".sc-tree-row-more", {
    display: "flex",
    alignItems: "center",
    width: "100%",
    minHeight: "var(--sc-control-min)",
    paddingBlock: "4px",
    border: "0",
    background: "transparent",
    textAlign: "start",
    cursor: "pointer",
    fontSize: "var(--mdui-typescale-label-large-size, .875rem)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)"
});
globalStyle(".sc-tree-row-twisty:focus-visible, .sc-tree-row-label:focus-visible, .sc-tree-row-more:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-tree-row, .sc-tree-row-twisty, .sc-tree-row-twisty-icon", {
    "@media": {
        "(prefers-reduced-motion: reduce)": {
            transition: "none"
        }
    }
});
