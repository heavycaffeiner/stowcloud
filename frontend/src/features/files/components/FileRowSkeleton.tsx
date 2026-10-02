import * as styles from './FileRowSkeleton.css'
import { cx } from '../../../ui/cx'

export function FileRowSkeleton({ rowIndex }: { rowIndex: number }) {
  return (
    <div className={styles.root} role="row" aria-rowindex={rowIndex} aria-busy="true">
      <span className={cx(styles.cell, styles.cellSelect)} role="gridcell" />
      <span className={cx(styles.cell, styles.cellName)} role="gridcell">
        <span className={cx(styles.bar, styles.barIcon)} />
        <span className={cx(styles.bar, styles.barName)} />
      </span>
      <span className={cx(styles.cell, styles.cellSize)} role="gridcell">
        <span className={cx(styles.bar, styles.barSize)} />
      </span>
      <span className={cx(styles.cell, styles.cellMtime)} role="gridcell">
        <span className={cx(styles.bar, styles.barMtime)} />
      </span>
    </div>
  )
}
