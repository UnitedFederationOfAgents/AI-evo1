// Spoken "application running" announcement when The Conversationalist
// starts or restarts (condocs/initialRobotImpls/Step5Prompt.md, Revision D),
// using the same browser speechSynthesis API as TC's notepad speak button.
//
// Kept identical across the-conversationalist's, local-representative's and
// agent-coordinator's frontends -- each feeds it the started_at (unix ms) TC
// reports, straight from its own message handler so it fires whichever page
// or tab the user is on.
//
// A start is announced once, and only if this page sees it happen:
//   - this page already saw an earlier started_at for the same TC, so the
//     process has restarted under it; or
//   - it's the first started_at this page has seen and it's under
//     FRESH_MS old -- covers a page that loaded (or reloaded, e.g. after a
//     rebuild bumped the version) while TC was down or just starting.
// A page that's hidden when the start is seen stays quiet -- nothing is
// queued for later. Browsers only allow speech once the user has interacted
// with the page, so a freshly (re)loaded page can't speak until clicked.

const FRESH_MS = 60_000
const PHRASE = 'application running'

const lastSeen = new Map<string, number>()

// noteTCStart records the started_at reported for the TC identified by key
// ('local' for a single TC, or a host id on agent-coordinator) and speaks
// the announcement if it marks a start this page should announce.
export function noteTCStart(key: string, startedAt: number | undefined) {
  if (!startedAt) return
  const prev = lastSeen.get(key)
  if (prev === startedAt) return
  lastSeen.set(key, startedAt)

  // abs(): the TC host's clock and this browser's may disagree a little.
  const fresh = Math.abs(Date.now() - startedAt) < FRESH_MS
  if (prev === undefined && !fresh) return

  // sessionStorage stops a reload from announcing the same start twice.
  const storageKey = `tc-announced:${key}:${startedAt}`
  if (sessionStorage.getItem(storageKey)) return
  sessionStorage.setItem(storageKey, '1')

  if (document.visibilityState !== 'visible') return
  if (!('speechSynthesis' in window)) return
  const utterance = new SpeechSynthesisUtterance(PHRASE)
  utterance.onerror = (e) => console.warn('application-running announcement not spoken:', e.error)
  window.speechSynthesis.speak(utterance)
}
