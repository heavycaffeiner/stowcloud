import '../../styles/features/files/browse-ui.css.ts'

export function FileRowSkeleton({ rowIndex }: { rowIndex: number }) {
  return (
    <div className="sc-row-skeleton" role="row" aria-rowindex={rowIndex} aria-busy="true">
      <span className="sc-row-skeleton-cell sc-row-skeleton-cell--select" role="gridcell" />
      <span className="sc-row-skeleton-cell sc-row-skeleton-cell--name" role="gridcell">
        <span className="sc-row-skeleton-bar sc-row-skeleton-bar--icon" />
        <span className="sc-row-skeleton-bar sc-row-skeleton-bar--name" />
      </span>
      <span className="sc-row-skeleton-cell sc-row-skeleton-cell--size" role="gridcell"><span className="sc-row-skeleton-bar sc-row-skeleton-bar--size" /></span>
      <span className="sc-row-skeleton-cell sc-row-skeleton-cell--mtime" role="gridcell"><span className="sc-row-skeleton-bar sc-row-skeleton-bar--mtime" /></span>
    </div>
  )
}
