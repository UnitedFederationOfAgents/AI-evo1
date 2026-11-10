import type { Diagram, DiagramSummary, DiagramView } from './types'

// The diagram API (../../api.go). URLs are built from the path this document
// was served under, as the WebSocket's is, so they also work beneath the
// plug-in route (/apps/dg/, /host/<id>/apps/dg/).
const dir = window.location.pathname.replace(/\/[^/]*\.[^/]*$/, '/')
export const basePath = dir.endsWith('/') ? dir.slice(0, -1) : dir

export function apiUrl(path: string): string {
  return `${basePath}${path}`
}

// request fetches JSON, turning a {error} answer into a thrown Error.
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(apiUrl(path), init)
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(body.error || `${path}: ${res.status} ${res.statusText}`)
  return body as T
}

export function listDiagrams(): Promise<DiagramSummary[]> {
  return request('/api/diagrams')
}

export function getDiagram(name: string): Promise<DiagramView> {
  return request(`/api/diagrams/${encodeURIComponent(name)}`)
}

// convert reads a DTT document, or writes one for a diagram the canvas has
// changed; either way the answer carries all three forms.
export function convert(from: { dtt: string } | { diagram: Diagram }): Promise<DiagramView> {
  return request('/api/convert', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(from),
  })
}
