import { useNavigate } from 'react-router-dom'
import { useLogout } from '../api'

/** Ends the session and leaves: single sign-on through the provider's own URL, a local session to sign-in. */
export function useSignOut() {
  const logout = useLogout()
  const navigate = useNavigate()
  const signOut = (): void =>
    logout.mutate(undefined, {
      onSettled: (result) => {
        if (result?.end_session_url) window.location.assign(result.end_session_url)
        else void navigate('/login', { replace: true })
      }
    })
  return { signOut, pending: logout.isPending }
}
