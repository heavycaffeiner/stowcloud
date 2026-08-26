// What an SMB republish did, as a sentence.
//
// Every write that changes what SMB should serve republishes, and the write
// never fails when the republish does: the row is committed and this server is
// already serving the change, so refusing would report a change that happened
// as one that did not. What was missing was saying so at the moment it
// happens, instead of on the health page whenever somebody next looked.
//
// This is the one place that turns the outcome into text, so the share screen
// and the grant screen cannot word the same fact differently.
import { t } from '../i18n'
import type { SMBOutcome } from './types'

/** The sentence for an outcome, or null when there is nothing worth saying:
 *  no sidecar, or an apply that went through cleanly. A screen that reported
 *  every successful republish would be noise on every rename. */
export function smbOutcomeText(outcome: SMBOutcome | undefined): string | null {
  if (!outcome) return null
  if (outcome.state === 'unreachable') {
    // The socket is named. "Rendered and nothing applied it" and "the agent
    // answered with a failure" are different things to go and look at.
    return t('smb.saved_but_not_applied', { socket: outcome.socket ?? '' })
  }
  if (outcome.state !== 'warnings') return null

  const parts: string[] = []
  const report = outcome.report
  if (report?.missingPaths?.length) {
    parts.push(t('smb.missing_paths', { paths: report.missingPaths.join(', ') }))
  }
  if (report?.missingCredentials?.length) {
    parts.push(t('smb.missing_credentials', { names: report.missingCredentials.join(', ') }))
  }
  // The agent's own message when it named something this build has no field
  // for. Falling through to it beats reporting a warning with nothing in it.
  if (parts.length === 0 && report?.message) parts.push(report.message)
  if (parts.length === 0) return null
  return t('smb.applied_with_warnings', { detail: parts.join('; ') })
}
