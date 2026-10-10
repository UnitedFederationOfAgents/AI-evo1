// Smart detection of what kind of text was copied, so the files tab's "new
// file from clipboard" dialog (Step4SubstepDPrompt.md Revisions D/E) can
// default to e.g. "clipboard-….json" rather than always ".txt". Everything
// here is a heuristic over the text alone: the checks run from the strictest
// (JSON actually parses) to the loosest (YAML/Markdown/CSV shape), and the
// first match wins. YAML goes before Markdown because its "# comment" lines
// look like headings, and CSV goes last because prose with a comma per line
// can pass for it. Anything unrecognised stays plain text.

export interface TextFormat {
  ext: string
  mime: string
  label: string // shown in the naming dialog, e.g. "JSON"
}

const fmt = (ext: string, mime: string, label: string): TextFormat => ({ ext, mime, label })

export const PLAIN_TEXT = fmt('txt', 'text/plain', 'plain text')
const JSON_FMT = fmt('json', 'application/json', 'JSON')
const JSONL_FMT = fmt('jsonl', 'application/jsonl', 'JSON Lines')
const XML_FMT = fmt('xml', 'application/xml', 'XML')
const SVG_FMT = fmt('svg', 'image/svg+xml', 'SVG')
const HTML_FMT = fmt('html', 'text/html', 'HTML')
const YAML_FMT = fmt('yaml', 'application/yaml', 'YAML')
const TOML_FMT = fmt('toml', 'application/toml', 'TOML')
const CSV_FMT = fmt('csv', 'text/csv', 'CSV')
const TSV_FMT = fmt('tsv', 'text/tab-separated-values', 'TSV')
const MD_FMT = fmt('md', 'text/markdown', 'Markdown')
const SH_FMT = fmt('sh', 'text/x-shellscript', 'shell script')
const PY_FMT = fmt('py', 'text/x-python', 'Python')
const JS_FMT = fmt('js', 'text/javascript', 'JavaScript')

// DETECT_LIMIT caps how much of a huge paste the line-based checks look at;
// JSON is still parsed whole, since a truncated document never parses.
const DETECT_LIMIT = 64 * 1024

export function detectTextFormat(text: string): TextFormat {
  const t = text.trim()
  if (!t) return PLAIN_TEXT
  const head = t.length > DETECT_LIMIT ? t.slice(0, t.lastIndexOf('\n', DETECT_LIMIT) + 1 || DETECT_LIMIT) : t
  const lines = head.split(/\r?\n/)
  return (
    detectShebang(lines[0]) ??
    detectJSON(t, lines) ??
    detectMarkup(head) ??
    detectTOML(lines) ??
    detectYAML(lines) ??
    detectMarkdown(lines) ??
    detectDelimited(lines) ??
    PLAIN_TEXT
  )
}

// A "#!" first line names its interpreter.
function detectShebang(first: string): TextFormat | null {
  if (!first.startsWith('#!')) return null
  if (/python/.test(first)) return PY_FMT
  if (/\b(node|deno|bun)\b/.test(first)) return JS_FMT
  return SH_FMT
}

// JSON only counts when it actually parses as an object or array (a bare
// "42" or "true" is just text); JSON Lines is two or more lines that each do.
function detectJSON(t: string, lines: string[]): TextFormat | null {
  if (t[0] !== '{' && t[0] !== '[') return null
  if (isJSONContainer(t)) return JSON_FMT
  const rows = lines.map(l => l.trim()).filter(Boolean)
  if (rows.length >= 2 && rows.every(isJSONContainer)) return JSONL_FMT
  return null
}

function isJSONContainer(s: string): boolean {
  if (s[0] !== '{' && s[0] !== '[') return false
  try {
    const v: unknown = JSON.parse(s)
    return typeof v === 'object' && v !== null
  } catch {
    return false
  }
}

// HTML_ROOT is a leading element that marks a fragment as HTML rather than
// XML. Only the first element counts, so an XML document that happens to
// contain an <a> or <p> further in stays XML.
const HTML_ROOT = /^<(html|head|body|div|span|p|a|ul|ol|li|table|tr|td|th|h[1-6]|br|img|script|style|section|article|nav|header|footer|main|form|input|button|pre|code|blockquote)\b/i

// detectMarkup: a doctype/<html> or a well-known HTML first element makes it
// HTML, an <svg> root makes it SVG, and otherwise any balanced tag tree is
// XML. An <?xml ?> declaration rules out HTML.
function detectMarkup(t: string): TextFormat | null {
  if (t[0] !== '<') return null
  if (/^<!doctype\s+html/i.test(t)) return HTML_FMT
  const decl = /^<\?xml\b/.test(t)
  const body = t.replace(/^<\?xml[\s\S]*?\?>\s*/, '').replace(/^(<!--[\s\S]*?-->\s*)+/, '')
  if (/^<svg\b/i.test(body)) return SVG_FMT
  if (!decl && HTML_ROOT.test(body)) return HTML_FMT
  return isBalancedMarkup(t) ? XML_FMT : null
}

// isBalancedMarkup is a light well-formedness check: every opening tag is
// closed in order, ignoring comments, CDATA, processing instructions and
// doctypes. It doesn't validate attributes or entities.
function isBalancedMarkup(t: string): boolean {
  const s = t.replace(/<!--[\s\S]*?-->|<!\[CDATA\[[\s\S]*?\]\]>|<\?[\s\S]*?\?>|<![^>]*>/g, '')
  const stack: string[] = []
  let tags = 0
  for (const m of s.matchAll(/<(\/?)([A-Za-z_][\w:.-]*)[^>]*?(\/?)>/g)) {
    tags++
    if (m[3]) continue // self-closing
    if (!m[1]) stack.push(m[2])
    else if (stack.pop() !== m[2]) return false
  }
  return tags > 0 && stack.length === 0
}

// detectDelimited: two or more rows that all split into the same number (2+)
// of fields. Tabs are tried first, since that's what copying cells out of a
// spreadsheet gives.
function detectDelimited(lines: string[]): TextFormat | null {
  const rows = lines.filter(l => l.trim())
  if (rows.length < 2) return null
  for (const [sep, f] of [['\t', TSV_FMT], [',', CSV_FMT]] as const) {
    const n = splitFields(rows[0], sep)
    if (n >= 2 && rows.every(r => splitFields(r, sep) === n)) return f
  }
  return null
}

// splitFields counts sep-separated fields, honouring "double-quoted" fields
// (with "" escapes) the way CSV does. -1 means an unterminated quote.
function splitFields(line: string, sep: string): number {
  let n = 1
  let quoted = false
  for (let i = 0; i < line.length; i++) {
    const c = line[i]
    if (quoted) {
      if (c === '"') {
        if (line[i + 1] === '"') i++
        else quoted = false
      }
    } else if (c === '"') quoted = true
    else if (c === sep) n++
  }
  return quoted ? -1 : n
}

const TOML_TABLE = /^\[\[?[A-Za-z0-9_.\-" ]+\]\]?$/
const TOML_KV = /^[A-Za-z0-9_.\-"]+\s*=\s*("|'|\[|\{|[+-]?\d|true\b|false\b|inf\b|nan\b)/

// detectTOML: every non-comment line is a [table] header or a key = value
// whose value looks like a TOML literal, with at least two key/value lines.
// (Lines inside a multi-line array or string aren't followed, so those
// documents fall back to plain text.)
function detectTOML(lines: string[]): TextFormat | null {
  let kv = 0
  for (const raw of lines) {
    const l = raw.trim()
    if (!l || l.startsWith('#')) continue
    if (TOML_KV.test(l)) kv++
    else if (!TOML_TABLE.test(l)) return null
  }
  return kv >= 2 ? TOML_FMT : null
}

// detectMarkdown needs two different Markdown signals (a heading and a
// list, say), or a fenced code block, so ordinary prose with one "- " line
// isn't caught.
function detectMarkdown(lines: string[]): TextFormat | null {
  const signals = new Set<string>()
  for (const l of lines) {
    if (/^#{1,6}\s+\S/.test(l)) signals.add('heading')
    else if (/^\s*(```|~~~)/.test(l)) signals.add('fence')
    else if (/^\s*([-*+]|\d+\.)\s+\S/.test(l)) signals.add('list')
    else if (/^\s*>\s?/.test(l)) signals.add('quote')
    else if (/^\s*\|.*\|\s*$/.test(l) && /-{3}/.test(l)) signals.add('table')
    if (/\[[^\]]+\]\([^)\s]+\)/.test(l)) signals.add('link')
    if (/(\*\*|__)\S.*?\S\1|`[^`\s][^`]*`/.test(l)) signals.add('emphasis')
  }
  return signals.has('fence') || signals.size >= 2 ? MD_FMT : null
}

const YAML_KEY = /^(\s*)(-\s+)?("[^"]*"|'[^']*'|[A-Za-z_][\w.\-/]*)\s*:(\s|$)/
const YAML_ITEM = /^\s*-(\s|$)/

// detectYAML: an explicit "---" start, or every top-level line being a
// "key: value" / "key:" (or "- item" under one) with at least two keys.
// Indented lines are taken as nested values, so a block of prose with one
// "Note: …" line doesn't qualify -- its other lines aren't keys.
function detectYAML(lines: string[]): TextFormat | null {
  const body = lines.filter(l => l.trim() && !l.trimStart().startsWith('#'))
  if (!body.length) return null
  if (body[0].trim() === '---' && body.length > 1 && body.slice(1).some(l => YAML_KEY.test(l))) return YAML_FMT
  let keys = 0
  for (const l of body) {
    if (YAML_KEY.test(l)) keys++
    else if (/^\s/.test(l) || YAML_ITEM.test(l)) continue
    else if (l.trim() === '---' || l.trim() === '...') continue
    else return null
  }
  return keys >= 2 && YAML_KEY.test(body[0]) ? YAML_FMT : null
}
