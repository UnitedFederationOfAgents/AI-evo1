import type { ReactNode } from 'react'
import { isLocalURL } from './types'

// RepoLink opens a repo's page on GitHub in a new tab. A local repo's link
// (ufa-local:...) has no page, so it is its text, with the link as a tooltip.
export function RepoLink({ href, children }: { href: string; children: ReactNode }) {
  if (isLocalURL(href)) return <span className="local-link" title={href}>{children}</span>
  return <a href={href} target="_blank" rel="noreferrer">{children}</a>
}
