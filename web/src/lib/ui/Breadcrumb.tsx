import { useI18n } from '../i18n/use-i18n'

export interface BreadcrumbCrumb {
  label: string
  path: string
}

export interface BreadcrumbProps {
  crumbs: BreadcrumbCrumb[]
  onNavigate?: (path: string) => void
  onnavigate?: (path: string) => void
}

export function Breadcrumb({ crumbs, onNavigate, onnavigate }: BreadcrumbProps) {
  const { t } = useI18n()
  const navigate = onNavigate ?? onnavigate
  return (
    <nav className="sc-breadcrumb" aria-label={t('breadcrumb.path')}>
      <ol className="sc-breadcrumb__list">
        {crumbs.map((crumb, index) => (
          <li className="sc-breadcrumb__item" key={crumb.path}>
            {index < crumbs.length - 1 ? (
              <>
                <button type="button" className="sc-breadcrumb__link" onClick={() => navigate?.(crumb.path)}>{crumb.label}</button>
                <span className="sc-breadcrumb__sep" aria-hidden="true">/</span>
              </>
            ) : <span className="sc-breadcrumb__current" aria-current="page">{crumb.label}</span>}
          </li>
        ))}
      </ol>
    </nav>
  )
}
