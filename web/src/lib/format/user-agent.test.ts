import { describe, expect, it } from 'vitest'
import { describeUserAgent } from './user-agent'

describe('describeUserAgent', () => {
  it('falls back to the placeholder for null/empty', () => {
    expect(describeUserAgent(null)).toBe('알 수 없는 기기')
    expect(describeUserAgent(undefined)).toBe('알 수 없는 기기')
    expect(describeUserAgent('   ')).toBe('알 수 없는 기기')
  })

  it('parses a Windows Chrome UA', () => {
    const ua =
      'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36'
    expect(describeUserAgent(ua)).toBe('Windows · Chrome')
  })

  it('parses a headless Chrome UA distinctly from real Chrome', () => {
    const ua =
      'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/150.0.0.0 Safari/537.36'
    expect(describeUserAgent(ua)).toBe('Windows · Chrome (headless)')
  })

  it('parses macOS Safari without misreading it as Chrome', () => {
    const ua =
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15'
    expect(describeUserAgent(ua)).toBe('macOS · Safari')
  })

  it('parses Edge ahead of the Chrome token it also carries', () => {
    const ua =
      'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0'
    expect(describeUserAgent(ua)).toBe('Windows · Edge')
  })

  it('parses Firefox on Linux', () => {
    const ua = 'Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0'
    expect(describeUserAgent(ua)).toBe('Linux · Firefox')
  })

  it('parses Android Chrome and iOS Safari', () => {
    expect(
      describeUserAgent(
        'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36'
      )
    ).toBe('Android · Chrome')
    expect(
      describeUserAgent(
        'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1'
      )
    ).toBe('iOS · Safari')
  })

  it('falls back to the raw string when nothing recognizable matches', () => {
    expect(describeUserAgent('rclone/v1.66.0')).toBe('rclone/v1.66.0')
  })

  it('falls back to just the OS or just the browser when only one side parses', () => {
    expect(describeUserAgent('Windows-only-marker with no known browser token')).toBe('Windows')
  })
})
