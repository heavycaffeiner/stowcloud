import './browse-ui.css'

export function FileRowSkeleton({ rowIndex }: { rowIndex: number }) {
  return (
    <div className="sc-row-skeleton" role="row" aria-rowindex={rowIndex} aria-busy="true">
      <span className="sc-row-skeleton__cell sc-row-skeleton__cell--select" role="gridcell" />
      <span className="sc-row-skeleton__cell sc-row-skeleton__cell--name" role="gridcell">
        <span className="sc-row-skeleton__bar sc-row-skeleton__bar--icon" />
        <span className="sc-row-skeleton__bar sc-row-skeleton__bar--name" />
      </span>
      <span className="sc-row-skeleton__cell sc-row-skeleton__cell--size" role="gridcell"><span className="sc-row-skeleton__bar sc-row-skeleton__bar--size" /></span>
      <span className="sc-row-skeleton__cell sc-row-skeleton__cell--mtime" role="gridcell"><span className="sc-row-skeleton__bar sc-row-skeleton__bar--mtime" /></span>
    </div>
  )
}
