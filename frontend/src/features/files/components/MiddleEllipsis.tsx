import * as styles from './MiddleEllipsis.css'
import { cx } from '../../../ui/cx'

const FILENAME_SUFFIX_GRAPHEMES = 8
const filenameSegmenter = new Intl.Segmenter(undefined, { granularity: 'grapheme' })

export function MiddleEllipsis({ name, className }: { name: string; className: string }) {
  const graphemes = Array.from(filenameSegmenter.segment(name), ({ segment }) => segment)
  const split = Math.max(0, graphemes.length - FILENAME_SUFFIX_GRAPHEMES)
  return (
    <span className={cx(className, styles.root)} title={name}>
      <bdi className={styles.start}>{graphemes.slice(0, split).join('')}</bdi>
      <bdi className={styles.end}>{graphemes.slice(split).join('')}</bdi>
    </span>
  )
}
