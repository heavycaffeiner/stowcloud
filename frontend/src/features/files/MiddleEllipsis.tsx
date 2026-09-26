import '../../styles/features/files/middle-ellipsis.css.ts'

const FILENAME_SUFFIX_GRAPHEMES = 8
const filenameSegmenter = new Intl.Segmenter(undefined, { granularity: 'grapheme' })

export function MiddleEllipsis({ name, className }: { name: string; className: string }) {
  const graphemes = Array.from(filenameSegmenter.segment(name), ({ segment }) => segment)
  const split = Math.max(0, graphemes.length - FILENAME_SUFFIX_GRAPHEMES)
  return <span className={`${className} sc-middle-ellipsis`} title={name}>
    <bdi className="sc-middle-ellipsis-start">{graphemes.slice(0, split).join('')}</bdi>
    <bdi className="sc-middle-ellipsis-end">{graphemes.slice(split).join('')}</bdi>
  </span>
}
